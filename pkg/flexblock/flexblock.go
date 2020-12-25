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
    "errors"
    "fmt"
    "io"
    "io/ioutil"
    "os"
    "path/filepath"
    "strings"
    "strconv"
    "time"
    "regexp"

    "github.com/golang/glog"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
    // "k8s.io/kubernetes/pkg/volume/util/volumepathhandler"
    utilexec "k8s.io/utils/exec"

    timestamp "github.com/golang/protobuf/ptypes/timestamp"
)

const (
    kib    int64 = 1024
    mib    int64 = kib * 1024
    gib    int64 = mib * 1024
    gib100 int64 = gib * 100
    tib    int64 = gib * 1024
    tib100 int64 = tib * 100
)

type flexBlock struct {
    name              string
    nodeID            string
    version           string
    endpoint          string
    ephemeral         bool
    maxVolumesPerNode int64

    ids *identityServer
    ns  *nodeServer
    cs  *controllerServer
}

type flexBlockVolume struct {
    VolName       string     `json:"volName"`
    VolID         string     `json:"volID"`
    VolSize       int64      `json:"volSize"`
    VolPath       string     `json:"volPath"`
    VolAccessType accessType `json:"volAccessType"`
    ParentVolID   string     `json:"parentVolID,omitempty"`
    ParentSnapID  string     `json:"parentSnapID,omitempty"`
    Ephemeral     bool       `json:"ephemeral"`
}

type flexBlockSnapshot struct {
    Name         string               `json:"name"`
    Id           string               `json:"id"`
    VolID        string               `json:"volID"`
    Path         string               `json:"path"`
    CreationTime *timestamp.Timestamp `json:"creationTime"`
    SizeBytes    int64                `json:"sizeBytes"`
    ReadyToUse   bool                 `json:"readyToUse"`
}

var (
    vendorVersion = "dev"

    flexBlockVolumes         map[string]flexBlockVolume
    flexBlockVolumeSnapshots map[string]flexBlockSnapshot
)

const (
    // Directory where data for volumes and snapshots are persisted.
    // This can be ephemeral within the container or persisted if
    // backed by a Pod volume.
    dataRoot = "/csi-data-dir"

    // Extension with which snapshot files will be saved.
    snapshotExt = ".snap"
)

func init() {
    flexBlockVolumes = map[string]flexBlockVolume{}
    flexBlockVolumeSnapshots = map[string]flexBlockSnapshot{}
}

func NewFlexBlockDriver(driverName, nodeID, endpoint string, ephemeral bool, maxVolumesPerNode int64, version string) (*flexBlock, error) {
    if driverName == "" {
        return nil, errors.New("no driver name provided")
    }

    if nodeID == "" {
        return nil, errors.New("no node id provided")
    }

    if endpoint == "" {
        return nil, errors.New("no driver endpoint provided")
    }
    if version != "" {
        vendorVersion = version
    }

    if err := os.MkdirAll(dataRoot, 0750); err != nil {
        return nil, fmt.Errorf("failed to create dataRoot: %v", err)
    }

    glog.Infof("Driver: %v ", driverName)
    glog.Infof("Version: %s", vendorVersion)

    return &flexBlock{
        name:              driverName,
        version:           vendorVersion,
        nodeID:            nodeID,
        endpoint:          endpoint,
        ephemeral:         ephemeral,
        maxVolumesPerNode: maxVolumesPerNode,
    }, nil
}

func getSnapshotID(file string) (bool, string) {
    glog.V(4).Infof("file: %s", file)
    // Files with .snap extension are volumesnapshot files.
    // e.g. foo.snap, foo.bar.snap
    if filepath.Ext(file) == snapshotExt {
        return true, strings.TrimSuffix(file, snapshotExt)
    }
    return false, ""
}

func discoverExistingSnapshots() {
    glog.V(4).Infof("discovering existing snapshots in %s", dataRoot)
    files, err := ioutil.ReadDir(dataRoot)
    if err != nil {
        glog.Errorf("failed to discover snapshots under %s: %v", dataRoot, err)
    }
    for _, file := range files {
        isSnapshot, snapshotID := getSnapshotID(file.Name())
        glog.V(4).Infof("find %s from file %s", snapshotID, dataRoot)
        if isSnapshot {
            glog.V(4).Infof("adding snapshot %s from file %s", snapshotID, getSnapshotPath(snapshotID))
            flexBlockVolumeSnapshots[snapshotID] = flexBlockSnapshot{
                Id:         snapshotID,
                Path:       getSnapshotPath(snapshotID),
                ReadyToUse: true,
            }
        } else {
            VolID := file.Name()
            glog.V(4).Infof("adding vol %s from %s", file.Name(), VolID)
            // flexblockVol := flexBlockVolume{
            //     VolID:         volID,
            //     VolName:       name,
            //     VolSize:       cap,
            //     VolPath:       path,
            //     VolAccessType: volAccessType,
            //     Ephemeral:     ephemeral,
            // }
            // flexBlockVolumes[volID] = flexblockVol
        }
    }
}

func (hp *flexBlock) Run() {
    // Create GRPC servers
    hp.ids = NewIdentityServer(hp.name, hp.version)
    hp.ns = NewNodeServer(hp.nodeID, hp.ephemeral, hp.maxVolumesPerNode)
    hp.cs = NewControllerServer(hp.ephemeral, hp.nodeID)

    discoverExistingSnapshots()
    s := NewNonBlockingGRPCServer()
    s.Start(hp.endpoint, hp.ids, hp.cs, hp.ns)
    s.Wait()
}

func getVolumeByID(volumeID string) (flexBlockVolume, error) {
    if flexBlockVol, ok := flexBlockVolumes[volumeID]; ok {
        return flexBlockVol, nil
    }
    return flexBlockVolume{}, fmt.Errorf("volume id %s does not exist in the volumes list", volumeID)
}

func getVolumeByName(volName string) (flexBlockVolume, error) {
    for _, flexBlockVol := range flexBlockVolumes {
        if flexBlockVol.VolName == volName {
            return flexBlockVol, nil
        }
    }
    return flexBlockVolume{}, fmt.Errorf("volume name %s does not exist in the volumes list", volName)
}

func getSnapshotByName(name string) (flexBlockSnapshot, error) {
    for _, snapshot := range flexBlockVolumeSnapshots {
        if snapshot.Name == name {
            return snapshot, nil
        }
    }
    return flexBlockSnapshot{}, fmt.Errorf("snapshot name %s does not exist in the snapshots list", name)
}

// getVolumePath returns the canonical path for flexblock volume
func getVolumePath(volID string) string {
    return filepath.Join(dataRoot, volID)
}

// createVolume create the directory for the flexblock volume.
// It returns the volume path or err if one occurs.
func createFlexblockVolume(volID, name string, cap int64, volAccessType accessType, ephemeral bool) (*flexBlockVolume, error) {
    glog.V(4).Infof("create flexblock volume: %s", volID)
    path := getVolumePath(volID)

    size := fmt.Sprintf("%dM", cap/mib)
    var cmd []string
    executor := utilexec.New()

    cmd = []string{"xioadm", "vdi", "create", volID, size}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return nil, status.Errorf(codes.Internal, "failed createflexblockvol %v: %v", err, string(out))
    }

    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)
    cmd = []string{"tgtadm", "-m", "target", "-o", "show"}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return nil, status.Errorf(codes.Internal, "failed createflexblockvol for andes error %v: %v", err, string(out))
    }
    if strings.Contains(string(out), iqnname) {
        return nil, status.Errorf(codes.Internal, "failed createflexblockvol find %v in target ", volID)
    } else {
            
        lasttid := 0
        tgtinfolastindex := strings.LastIndex(string(out), "Target ")
        if tgtinfolastindex != -1 {
            lasttidinfo := string(out)[tgtinfolastindex:]

            // reg := regexp.MustCompile("Target [\\d]+: iqn.")
            // MEId := reg.FindString(lasttidinfo)

            reg1 := regexp.MustCompile("Target [\\d]+:")
            MEId1 := reg1.FindString(lasttidinfo)
            // l3:=strings.Count(MEId1,"")-1
            l3 := len(MEId1)

            lasttid1,_ := strconv.Atoi(MEId1[7:l3-1])
            lasttid = lasttid1
        }

        tid := int(lasttid+1)
        tidstr := strconv.Itoa(tid)
        cmd = []string{"tgtadm", "-L", "iscsi", "-m", "target", "-o", "new", "-t", tidstr, "-T", iqnname}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for target error %v: %v", err, string(out))
        }
        
        andessockpath := fmt.Sprintf("unix:///var/lib/flex/7000/sock:%s", volID)
        cmd = []string{"tgtadm", "-m", "lu", "-o", "new", "-t", tidstr, "-l", "1", "-E", "flexblock", "-b", andessockpath}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for lun error %v: %v", err, string(out))
        }

        cmd = []string{"tgtadm", "-L", "iscsi", "-o", "bind", "-m", "target", "-t", tidstr, "-I", "127.0.0.1"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for target bind localhost error %v: %v", err, string(out))
        }

        cmd = []string{"nc3kxtarget", "--dump"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for dump target error %v: %v", err, string(out))
        }

        cmd = []string{"iscsiadm", "-m", "discovery", "-t", "st", "-p", "127.0.0.1"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for discovery target error %v: %v", err, string(out))
        }

        cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-p", "127.0.0.1", "-l"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for login target error %v: %v", err, string(out))
        }

        // disk path  /dev/disk/by-path/ip-127.0.0.1\:3260-iscsi-iqn.2017-10-30.kx.flexnfs-nfs01-lun-2
    }
    diskpath :=  fmt.Sprintf("/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-%s-lun-1", iqnname)



    switch volAccessType {
    case mountAccess:
        err := os.MkdirAll(path, 0777)
        if err != nil {
            return nil, err
        }

        time.Sleep(time.Duration(5)*time.Second)
        cmd = []string{"mkfs.ext4", diskpath, "-F"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for mkfs ext4 error %v: %v", err, string(out))
        }

        cmd = []string{"mount", "-t", "ext4", diskpath, path}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for mount target error %v: %v", err, string(out))
        }

    case blockAccess:
        // executor := utilexec.New()
        // // size := fmt.Sprintf("%dM", cap/mib)
        // // Create a block file.
        // _, err := os.Stat(path)
        // if err != nil {
        //     if os.IsNotExist(err) {
        //         out, err := executor.Command("fallocate", "-l", size, path).CombinedOutput()
        //         if err != nil {
        //             return nil, fmt.Errorf("failed to create block device: %v, %v", err, string(out))
        //         }
        //     } else {
        //         return nil, fmt.Errorf("failed to stat block device: %v, %v", path, err)
        //     }
        // }

        // // Associate block file with the loop device.
        // volPathHandler := volumepathhandler.VolumePathHandler{}
        // _, err = volPathHandler.AttachFileDevice(path)
        // if err != nil {
        //     // Remove the block file because it'll no longer be used again.
        //     if err2 := os.Remove(path); err2 != nil {
        //         glog.Errorf("failed to cleanup block file %s: %v", path, err2)
        //     }
        //     return nil, fmt.Errorf("failed to attach device %v: %v", path, err)
        // }
        path = diskpath
    default:
        return nil, fmt.Errorf("unsupported access type %v", volAccessType)
    }

    flexblockVol := flexBlockVolume{
        VolID:         volID,
        VolName:       name,
        VolSize:       cap,
        VolPath:       path,
        VolAccessType: volAccessType,
        Ephemeral:     ephemeral,
    }
    flexBlockVolumes[volID] = flexblockVol
    return &flexblockVol, nil
}

// updateVolume updates the existing flexblock volume.
func updateFlexblockVolume(volID string, volume flexBlockVolume) error {
    glog.V(4).Infof("updating flexblock volume: %s", volID)

    vol, err := getVolumeByID(volID)
    if err != nil {
        // Return OK if the volume is not found.
        return nil
    }

    size := fmt.Sprintf("%dM", volume.VolSize/mib)
    var cmd []string
    executor := utilexec.New()
    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)

    cmd = []string{"xioadm", "vdi", "resize", volID, size}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed updateflexblockvol %v: %v", err, string(out))
    }

    cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-R"}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed updateflexblockvol update target %v: %v", err, string(out))
    }

    diskpath :=  fmt.Sprintf("/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-%s-lun-1", iqnname)
    if vol.VolAccessType == mountAccess {
        cmd = []string{"resize2fs", diskpath}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return status.Errorf(codes.Internal, "failed updateflexblockvol resize fs target %v: %v", err, string(out))
        }
    }

    flexBlockVolumes[volID] = volume
    return nil
}

// deleteVolume deletes the directory for the flexblock volume.
func deleteFlexblockVolume(volID string) error {
    glog.V(4).Infof("deleting flexblock volume: %s", volID)

    vol, err := getVolumeByID(volID)
    if err != nil {
        // Return OK if the volume is not found.
        return nil
    }

    //if vol.VolAccessType == blockAccess {
    //    volPathHandler := volumepathhandler.VolumePathHandler{}
    //    path := getVolumePath(volID)
    //    glog.V(4).Infof("deleting loop device for file %s if it exists", path)
    //    if err := volPathHandler.DetachFileDevice(path); err != nil {
    //        return fmt.Errorf("failed to remove loop device for file %s: %v", path, err)
    //    }
    //}

    path := getVolumePath(volID)

    var cmd []string
    executor := utilexec.New()
    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)

    if vol.VolAccessType == mountAccess {
        // umount 
        cmd = []string{"umount", path}
        glog.V(4).Infof("Command Start: %v", cmd)
        outumount, errumount := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(outumount))
        if errumount != nil {
            return status.Errorf(codes.Internal, "failed deleteflexblockvol for umount target error %v: %v", err, string(outumount))
        }

    }

    cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-u"}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed deleteflexblockvol for logout target error %v: %v", err, string(out))
    }

    cmd = []string{"iscsiadm", "-m", "discoverydb", "-n", iqnname, "-o", "delete", "-t", "st", "-p", "127.0.0.1"}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed deleteflexblockvol for discoverydb delete target error %v: %v", err, string(out))
    }

    cmd = []string{"tgtadm", "-m", "target", "-o", "show"}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed deleteflexblockvol for andes error %v: %v", err, string(out))
    }
    if !strings.Contains(string(out), iqnname) {
        return status.Errorf(codes.Internal, "failed deleteflexblockvol find %v in target ", volID)
    } else {
        reg := regexp.MustCompile("Target [\\d]+: "+iqnname)
        MEId := reg.FindString(string(out))

        reg1 := regexp.MustCompile("Target [\\d]+:")
        MEId1 := reg1.FindString(MEId)
        l3:=strings.Count(MEId1,"")-1

         
        lasttid := 0
        tgtinfolastindex := strings.LastIndex(MEId, MEId1)
        if tgtinfolastindex != -1 {
            lasttid1,_ := strconv.Atoi(MEId1[tgtinfolastindex+7:l3-1])
            lasttid = lasttid1
        }

        tidstr := strconv.Itoa(lasttid)
        // tgtadm -L iscsi -o delete -m target -t $tid
        cmd = []string{"tgtadm", "-L", "iscsi", "-m", "target", "-o", "delete", "-t", tidstr}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return status.Errorf(codes.Internal, "failed deleteflexblockvol for delete target error %v: %v", err, string(out))
        }

        cmd = []string{"nc3kxtarget", "--dump"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return status.Errorf(codes.Internal, "failed deleteflexblockvol for dump target error %v: %v", err, string(out))
        }

    }

    cmd = []string{"xioadm", "vdi", "delete", volID}
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed deleteflexblockvol %v: %v", err, string(out))
    }

    if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
        return err
    }
    delete(flexBlockVolumes, volID)
    return nil
}

// flexBlockIsEmpty is a simple check to determine if the specified flexblock directory
// is empty or not.
func flexBlockIsEmpty(p string) (bool, error) {
    f, err := os.Open(p)
    if err != nil {
        return true, fmt.Errorf("unable to open flexblock volume, error: %v", err)
    }
    defer f.Close()

    _, err = f.Readdir(1)
    if err == io.EOF {
        return true, nil
    }
    return false, err
}

// loadFromSnapshot populates the given destPath with data from the snapshotID
func loadFromSnapshot(size int64, snapshotId, destPath string, mode accessType) error {
    snapshot, ok := flexBlockVolumeSnapshots[snapshotId]
    if !ok {
        return status.Errorf(codes.NotFound, "cannot find snapshot %v", snapshotId)
    }
    if snapshot.ReadyToUse != true {
        return status.Errorf(codes.Internal, "snapshot %v is not yet ready to use.", snapshotId)
    }
    if snapshot.SizeBytes > size {
        return status.Errorf(codes.InvalidArgument, "snapshot %v size %v is greater than requested volume size %v", snapshotId, snapshot.SizeBytes, size)
    }
    snapshotPath := snapshot.Path

    var cmd []string
    switch mode {
    case mountAccess:
        cmd = []string{"tar", "zxvf", snapshotPath, "-C", destPath}
    case blockAccess:
        cmd = []string{"dd", "if=" + snapshotPath, "of=" + destPath}
    default:
        return status.Errorf(codes.InvalidArgument, "unknown accessType: %d", mode)
    }

    executor := utilexec.New()
    glog.V(4).Infof("Command Start: %v", cmd)
    out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
    glog.V(4).Infof("Command Finish: %v", string(out))
    if err != nil {
        return status.Errorf(codes.Internal, "failed pre-populate data from snapshot %v: %v: %s", snapshotId, err, out)
    }
    return nil
}

// loadFromVolume populates the given destPath with data from the srcVolumeID
func loadFromVolume(size int64, srcVolumeId, destPath string, mode accessType) error {
    flexBlockVolume, ok := flexBlockVolumes[srcVolumeId]
    if !ok {
        return status.Error(codes.NotFound, "source volumeId does not exist, are source/destination in the same storage class?")
    }
    if flexBlockVolume.VolSize > size {
        return status.Errorf(codes.InvalidArgument, "volume %v size %v is greater than requested volume size %v", srcVolumeId, flexBlockVolume.VolSize, size)
    }
    if mode != flexBlockVolume.VolAccessType {
        return status.Errorf(codes.InvalidArgument, "volume %v mode is not compatible with requested mode", srcVolumeId)
    }

    switch mode {
    case mountAccess:
        return loadFromFilesystemVolume(flexBlockVolume, destPath)
    case blockAccess:
        return loadFromBlockVolume(flexBlockVolume, destPath)
    default:
        return status.Errorf(codes.InvalidArgument, "unknown accessType: %d", mode)
    }
}

func loadFromFilesystemVolume(flexBlockVolume flexBlockVolume, destPath string) error {
    srcPath := flexBlockVolume.VolPath
    isEmpty, err := flexBlockIsEmpty(srcPath)
    if err != nil {
        return status.Errorf(codes.Internal, "failed verification check of source flexblock volume %v: %v", flexBlockVolume.VolID, err)
    }

    // If the source flexblock volume is empty it's a noop and we just move along, otherwise the cp call will fail with a a file stat error DNE
    if !isEmpty {
        args := []string{"-a", srcPath + "/.", destPath + "/"}
        executor := utilexec.New()
        out, err := executor.Command("cp", args...).CombinedOutput()
        if err != nil {
            return status.Errorf(codes.Internal, "failed pre-populate data from volume %v: %v: %s", flexBlockVolume.VolID, err, out)
        }
    }
    return nil
}

func loadFromBlockVolume(flexBlockVolume flexBlockVolume, destPath string) error {
    srcPath := flexBlockVolume.VolPath
    args := []string{"if=" + srcPath, "of=" + destPath}
    executor := utilexec.New()
    out, err := executor.Command("dd", args...).CombinedOutput()
    if err != nil {
        return status.Errorf(codes.Internal, "failed pre-populate data from volume %v: %v: %s", flexBlockVolume.VolID, err, out)
    }
    return nil
}
