/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package flexblock

import (
    "fmt"
    "os"
    "strings"
    "time"

    "github.com/golang/glog"
    "golang.org/x/net/context"

    "github.com/container-storage-interface/spec/lib/go/csi"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    // "k8s.io/kubernetes/pkg/volume/util/volumepathhandler"
    "k8s.io/utils/mount"
    utilexec "k8s.io/utils/exec"
)

const TopologyKeyNode = "topology.flexblock.csi/node"

type nodeServer struct {
    nodeID            string
    ephemeral         bool
    maxVolumesPerNode int64
}

func NewNodeServer(nodeId string, ephemeral bool, maxVolumesPerNode int64) *nodeServer {
    return &nodeServer{
        nodeID:            nodeId,
        ephemeral:         ephemeral,
        maxVolumesPerNode: maxVolumesPerNode,
    }
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {

    // Check arguments
    if req.GetVolumeCapability() == nil {
        return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
    }
    if len(req.GetVolumeId()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if len(req.GetTargetPath()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
    }

    targetPath := req.GetTargetPath()
    ephemeralVolume := req.GetVolumeContext()["csi.storage.k8s.io/ephemeral"] == "true" ||
        req.GetVolumeContext()["csi.storage.k8s.io/ephemeral"] == "" && ns.ephemeral // Kubernetes 1.15 doesn't have csi.storage.k8s.io/ephemeral.

    if req.GetVolumeCapability().GetBlock() != nil &&
        req.GetVolumeCapability().GetMount() != nil {
        return nil, status.Error(codes.InvalidArgument, "cannot have both block and mount access type")
    }

    // if ephemeral is specified, create volume here to avoid errors
    if ephemeralVolume {
        volID := req.GetVolumeId()
        volName := fmt.Sprintf("ephemeral-%s", volID)
        vol, err := createFlexblockVolume(req.GetVolumeId(), volName, maxStorageCapacity, mountAccess, ephemeralVolume)
        if err != nil && !os.IsExist(err) {
            glog.Error("ephemeral mode failed to create volume: ", err)
            return nil, status.Error(codes.Internal, err.Error())
        }
        glog.V(4).Infof("ephemeral mode: created volume: %s", vol.VolPath)
    }

    vol, err := getVolumeByID(req.GetVolumeId())
    if err != nil {
        return nil, status.Error(codes.NotFound, err.Error())
    }

    if req.GetVolumeCapability().GetBlock() != nil {
        if vol.VolAccessType != blockAccess {
            return nil, status.Error(codes.InvalidArgument, "cannot publish a non-block volume as block volume")
        }

        glog.V(4).Infof("find  volume: %s", vol.VolPath)
        // volPathHandler := volumepathhandler.VolumePathHandler{}
        // 
        // // Get loop device from the volume path.
        // loopDevice, err := volPathHandler.GetLoopDevice(vol.VolPath)
        // if err != nil {
        //     return nil, status.Error(codes.Internal, fmt.Sprintf("failed to get the loop device: %v", err))
        // }
        loopDevice := vol.VolPath

        mounter := mount.New("")

        // Check if the target path exists. Create if not present.
        _, err = os.Lstat(targetPath)
        if os.IsNotExist(err) {
            if err = makeFile(targetPath); err != nil {
                return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create target path: %s: %v", targetPath, err))
            }
        }
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed to check if the target block file exists: %v", err)
        }

        // Check if the target path is already mounted. Prevent remounting.
        notMount, err := mount.IsNotMountPoint(mounter, targetPath)
        if err != nil {
            if !os.IsNotExist(err) {
                return nil, status.Errorf(codes.Internal, "error checking path %s for mount: %s", targetPath, err)
            }
            notMount = true
        }
        if !notMount {
            // It's already mounted.
            glog.V(5).Infof("Skipping bind-mounting subpath %s: already mounted", targetPath)
            return &csi.NodePublishVolumeResponse{}, nil
        }

        options := []string{"bind"}
        if err := mount.New("").Mount(loopDevice, targetPath, "", options); err != nil {
            return nil, status.Error(codes.Internal, fmt.Sprintf("failed to mount block device: %s at %s: %v", loopDevice, targetPath, err))
        }
    } else if req.GetVolumeCapability().GetMount() != nil {
        if vol.VolAccessType != mountAccess {
            return nil, status.Error(codes.InvalidArgument, "cannot publish a non-mount volume as mount volume")
        }

        notMnt, err := mount.IsNotMountPoint(mount.New(""), targetPath)
        if err != nil {
            if os.IsNotExist(err) {
                if err = os.MkdirAll(targetPath, 0750); err != nil {
                    return nil, status.Error(codes.Internal, err.Error())
                }
                notMnt = true
            } else {
                return nil, status.Error(codes.Internal, err.Error())
            }
        }

        if !notMnt {
            return &csi.NodePublishVolumeResponse{}, nil
        }

        fsType := req.GetVolumeCapability().GetMount().GetFsType()

        deviceId := ""
        if req.GetPublishContext() != nil {
            deviceId = req.GetPublishContext()[deviceID]
        }

        readOnly := req.GetReadonly()
        volumeId := req.GetVolumeId()
        attrib := req.GetVolumeContext()
        mountFlags := req.GetVolumeCapability().GetMount().GetMountFlags()

        glog.V(4).Infof("target %v\nfstype %v\ndevice %v\nreadonly %v\nvolumeId %v\nattributes %v\nmountflags %v\n",
            targetPath, fsType, deviceId, readOnly, volumeId, attrib, mountFlags)

        path := getVolumePath(volumeId)
        pathnotMnt, patherr := mount.IsNotMountPoint(mount.New(""), path)
        if patherr != nil {
            if os.IsNotExist(err) {
                if err = os.MkdirAll(targetPath, 0750); err != nil {
                    return nil, status.Error(codes.Internal, err.Error())
                }
                pathnotMnt = true
            }
        }
        if pathnotMnt {
            var cmd []string
            executor := utilexec.New()

            iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volumeId)

            cmd = []string{"iscsiadm", "-m", "discovery", "-t", "st", "-p", "127.0.0.1"}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
            if err != nil {
                return nil, status.Errorf(codes.Internal, "failed publishVolume for discovery target error %v: %v", err, string(out))
            }

            cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-p", "127.0.0.1", "-l"}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
            if err != nil {
                return nil, status.Errorf(codes.Internal, "failed publishVolume for login target error %v: %v", err, string(out))
            }

            cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-p", "127.0.0.1", "-R"}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
            if err != nil {
                return nil, status.Errorf(codes.Internal, "failed publishVolume for rescan target error %v: %v", err, string(out))
            }

            time.Sleep(time.Duration(5)*time.Second)

            // disk path  /dev/disk/by-path/ip-127.0.0.1\:3260-iscsi-iqn.2017-10-30.kx.flexnfs-nfs01-lun-2
            diskpath :=  fmt.Sprintf("/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-%s-lun-1", iqnname)

            cmd = []string{"mount", "-t", "ext4", diskpath, targetPath}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
            if err != nil {
                return nil, status.Errorf(codes.Internal, "failed publishflexblockvol for mount target error %v: %v", err, string(out))
            }


        } else {

            options := []string{"bind"}
            if readOnly {
                options = append(options, "ro")
            }
            mounter := mount.New("")

            if err := mounter.Mount(path, targetPath, "", options); err != nil {
                var errList strings.Builder
                errList.WriteString(err.Error())
               if vol.Ephemeral {
                    if rmErr := os.RemoveAll(path); rmErr != nil && !os.IsNotExist(rmErr) {
                        errList.WriteString(fmt.Sprintf(" :%s", rmErr.Error()))
                    }
                }
                return nil, status.Error(codes.Internal, fmt.Sprintf("failed to mount device: %s at %s: %s", path, targetPath, errList.String()))
            }
        }
    }

    return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {

    // Check arguments
    if len(req.GetVolumeId()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if len(req.GetTargetPath()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
    }
    targetPath := req.GetTargetPath()
    volumeID := req.GetVolumeId()

    vol, err := getVolumeByID(volumeID)
    if err != nil {
        return nil, status.Error(codes.NotFound, err.Error())
    }

    // Unmount only if the target path is really a mount point.
    if notMnt, err := mount.IsNotMountPoint(mount.New(""), targetPath); err != nil {
        if !os.IsNotExist(err) {
            return nil, status.Error(codes.Internal, err.Error())
        }
    } else if !notMnt {
        // Unmounting the image or filesystem.
        err = mount.New("").Unmount(targetPath)
        if err != nil {
            return nil, status.Error(codes.Internal, err.Error())
        }
    }
    // Delete the mount point.
    // Does not return error for non-existent path, repeated calls OK for idempotency.
    if err = os.RemoveAll(targetPath); err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }
    glog.V(4).Infof("flexblock: volume %s has been unpublished.", targetPath)

    if vol.Ephemeral {
        glog.V(4).Infof("deleting volume %s", volumeID)
        if err := deleteFlexblockVolume(volumeID); err != nil && !os.IsNotExist(err) {
            return nil, status.Error(codes.Internal, fmt.Sprintf("failed to delete volume: %s", err))
        }
    }

    return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {

    // Check arguments
    if len(req.GetVolumeId()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if len(req.GetStagingTargetPath()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
    }
    if req.GetVolumeCapability() == nil {
        return nil, status.Error(codes.InvalidArgument, "Volume Capability missing in request")
    }

    return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {

    // Check arguments
    if len(req.GetVolumeId()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
    }
    if len(req.GetStagingTargetPath()) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
    }

    return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {

    topology := &csi.Topology{
        Segments: map[string]string{TopologyKeyNode: ns.nodeID},
    }

    return &csi.NodeGetInfoResponse{
        NodeId:             ns.nodeID,
        MaxVolumesPerNode:  ns.maxVolumesPerNode,
        AccessibleTopology: topology,
    }, nil
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {

    return &csi.NodeGetCapabilitiesResponse{
        Capabilities: []*csi.NodeServiceCapability{
            {
                Type: &csi.NodeServiceCapability_Rpc{
                    Rpc: &csi.NodeServiceCapability_RPC{
                        Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
                    },
                },
            },
            {
                Type: &csi.NodeServiceCapability_Rpc{
                    Rpc: &csi.NodeServiceCapability_RPC{
                        Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
                    },
                },
            },
        },
    }, nil
}

func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, in *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
    return nil, status.Error(codes.Unimplemented, "")
}

// NodeExpandVolume is only implemented so the driver can be used for e2e testing.
func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {

    volID := req.GetVolumeId()
    if len(volID) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
    }

    vol, err := getVolumeByID(volID)
    if err != nil {
        // Assume not found error
        return nil, status.Errorf(codes.NotFound, "Could not get volume %s: %v", volID, err)
    }

    volPath := req.GetVolumePath()
    if len(volPath) == 0 {
        return nil, status.Error(codes.InvalidArgument, "Volume path not provided")
    }

    capRange := req.GetCapacityRange()
    if capRange == nil {
        return nil, status.Error(codes.InvalidArgument, "Capacity range not provided")
    }

    capacity := int64(capRange.GetRequiredBytes())
    if capacity >= maxStorageCapacity {
        return nil, status.Errorf(codes.OutOfRange, "Requested capacity %d exceeds maximum allowed %d", capacity, maxStorageCapacity)
    }

    info, err := os.Stat(volPath)
    if err != nil {
        return nil, status.Errorf(codes.InvalidArgument, "Could not get file information from %s: %v", volPath, err)
    }

    switch m := info.Mode(); {
    case m.IsDir():
        if vol.VolAccessType != mountAccess {
            return nil, status.Errorf(codes.InvalidArgument, "Volume %s is not a directory", volID)
        }
    case m&os.ModeDevice != 0:
        if vol.VolAccessType != blockAccess {
            return nil, status.Errorf(codes.InvalidArgument, "Volume %s is not a block device", volID)
        }
    default:
        return nil, status.Errorf(codes.InvalidArgument, "Volume %s is invalid", volID)
    }

    if vol.VolSize < capacity {
        vol.VolSize = capacity
        if err := updateFlexblockVolume(volID, vol); err != nil {
            return nil, status.Errorf(codes.Internal, "Could not update volume %s: %v", volID, err)
        }
    }

    return &csi.NodeExpandVolumeResponse{}, nil
}

// makeFile ensures that the file exists, creating it if necessary.
// The parent directory must exist.
func makeFile(pathname string) error {
    f, err := os.OpenFile(pathname, os.O_CREATE, os.FileMode(0644))
    defer f.Close()
    if err != nil {
        if !os.IsExist(err) {
            return err
        }
    }
    return nil
}
