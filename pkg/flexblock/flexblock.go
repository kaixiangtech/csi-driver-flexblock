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
    "bufio"
    "path/filepath"
    "strings"
    "strconv"
    "time"
    "regexp"
    "syscall"
    "encoding/json"
    "crypto/md5"
    "encoding/hex"
    "net/http"
    "net/url"

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
    confinfo                 map[string]string
)

const (
    // Directory where data for volumes and snapshots are persisted.
    // This can be ephemeral within the container or persisted if
    // backed by a Pod volume.
    dataRoot = "/csi-flexblock-data-dir"

    // Extension with which snapshot files will be saved.
    snapshotExt = ".snap"
)

func init() {
    flexBlockVolumes = map[string]flexBlockVolume{}
    flexBlockVolumeSnapshots = map[string]flexBlockSnapshot{}
    confinfo = map[string]string{}
    conffile := "/etc/neucli/flexblock_plugin.cfg"
    fconf, errconf := os.Open(conffile)
    if errconf != nil {
        glog.V(4).Infof("open plugin cfg file error : %v", errconf)
        return
    }
    confbuf := bufio.NewReader(fconf)
    for {
        line, readerr := confbuf.ReadString('\n')
        if readerr != nil {
            break
        }
        line = strings.TrimSpace(line)
        seqindex := strings.Index(line, "=")
        if seqindex > 0 {
            confinfo[line[:seqindex]] = line[seqindex+1:]
        }
    }
}

func createTarget(storclusterid int, sercluseterid int, volumename string, vid int) (int, int, error){
    tid := 0
    lunid := 0
    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    endpoint := "http://"+storagemngip+":"+storagemngport
    mngtoken := confinfo["mngtoken"]

    client := &http.Client{}

    createjson := fmt.Sprintf("{\"serviceName\":\"%s\",\"description\":\"\",\"accessFlag\":\"%s\",\"serClusterIdList\":[%d],\"clusterId\":%d}", volumename, volumename, sercluseterid, storclusterid)
    createpayload := strings.NewReader(createjson)
    gettokenuri := endpoint+"/mng/serTarget"
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err := http.NewRequest("POST", gettokenuri, createpayload)
    if err != nil{
        glog.V(4).Infof("new request create tid error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr := client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create tid error")
        return -1, 0, nil
    }

    gettokenuri = endpoint+"/mng/volume/iscsiService/serviceGroup?options=%7B%22serviceProto%22:%22iscsi%22%7D"
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err = http.NewRequest("GET", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request get tgt error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request get tgt error")
        return -1, 0, nil
    }
    if resp.StatusCode != 200 {
        glog.V(4).Infof("response get tgt info  status code not 200")
        return -1, 0, nil
    }
    defer resp.Body.Close()
    respbody, _ := ioutil.ReadAll(resp.Body)

    type Tidinfo struct {
        Id int `json:"id"`
        ServiceName string `json:"serviceName"`
        ClusterId int `json:"clusterId"`
        AccessFlag string `json:"accessFlag"`
        Creator string `json:"creator"`
        CreateTime string `json:"createTime"`
        ClusterName string `json:"clusterName"`
        SerClusterName string `json:"serClusterName"`
        OperatingStatus string `json:"operatingStatus"`
        ServiceProto string `json:"serviceProto"`
    }

    type ALLTidinfo struct {
        Result string `json:"result"`
        Message string `json:"message"`
        Params []Tidinfo `json:"params"`
    }
    var alltinfos ALLTidinfo

    json.Unmarshal([]byte(string(respbody)), &alltinfos)
    for i := 0; i < len(alltinfos.Params); i++ {
        if storclusterid == alltinfos.Params[i].ClusterId && confinfo["storagemngsercluster"] == alltinfos.Params[i].SerClusterName {
            if volumename == alltinfos.Params[i].AccessFlag {
                tid = alltinfos.Params[i].Id
                glog.V(4).Infof("find new tid %v", tid)
                break
            }
        }
    }

    createjson = fmt.Sprintf("[%d]", vid)
    createpayload = strings.NewReader(createjson)
    gettokenuri = fmt.Sprintf("%s/mng/serTarget/%d/bind?serviceProto=iscsi", endpoint, tid)
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err = http.NewRequest("POST", gettokenuri, createpayload)
    if err != nil{
        glog.V(4).Infof("new request create lunid error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create lunid error")
        return -1, 0, nil
    }
    for {
        time.Sleep(time.Duration(10)*time.Second)
        //gettokenuri = endpoint+"/mng/serTarget/"+string(tid)+"/bindVol"
        gettokenuri = fmt.Sprintf("%s/mng/serTarget/%d/bindVol", endpoint, tid)
        glog.V(4).Infof("new request create get lunid %v", gettokenuri)
        req, err = http.NewRequest("GET", gettokenuri, nil)
        if err != nil{
            glog.V(4).Infof("new request get lunid error")
            return -1, 0, nil
        }
        req.Header.Add("X_auth_token", mngtoken)
        req.Header.Add("Content-Type", "application/json")
        resp, resperr = client.Do(req)
        if resperr != nil{
            glog.V(4).Infof("request get lunid error")
            return -1, 0, nil
        }
        if resp.StatusCode != 200 {
            glog.V(4).Infof("response get lunid info  status code not 200")
            return -1, 0, nil
        }
        defer resp.Body.Close()
        respbody, _ := ioutil.ReadAll(resp.Body)

        type Luninfo struct {
            VolumeId int `json:"volumeId"`
            VolumeName string `json:"volumeName"`
            Status string `json:"status"`
            OperatingStatus string `json:"operatingStatus"`
            Description string `json:"description"`
            Wwn string `json:"wwn"`
            IsOperating bool `json:"isOperating"`
            MarkDel int `json:"markDel"`
            VolumeNum int `json:"volumeNum"`
            AutoPackSelect bool `json:"autoPackSelect"`
        }

        type ALLLuninfo struct {
            Result string `json:"result"`
            Params []Luninfo `json:"params"`
        }
        var alllinfos ALLLuninfo

        json.Unmarshal([]byte(string(respbody)), &alllinfos)
        for j := 0; j < len(alllinfos.Params); j++ {
            if volumename == alllinfos.Params[j].VolumeName {
                if "TargetNewOK" == alllinfos.Params[j].OperatingStatus {
                    lunid = alllinfos.Params[j].VolumeId
                    break
                }
            }
        }
        if lunid != 0 {
            break
        }
    }
    createjson = "{formerAcl:false,formerIp:[],formerName:[],laterAcl:true,laterIp:[],laterName:[],readonly:0,timeout:6}"
    createpayload = strings.NewReader(createjson)
    //gettokenuri = endpoint+"/mng/volume/iscsiService/"+string(tid)+"/service"
    gettokenuri = fmt.Sprintf("%s/mng/volume/iscsiService/%d/service", endpoint, tid)
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err = http.NewRequest("POST", gettokenuri, createpayload)
    if err != nil{
        glog.V(4).Infof("new request create lunid error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create lunid error")
        return -1, 0, nil
    }
    return tid, lunid, nil
}

func createVol(clusterid int, poolid int, volumename string, volsize int64) (int, int, error){
    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    endpoint := "http://"+storagemngip+":"+storagemngport
    mngtoken := confinfo["mngtoken"]

    client := &http.Client{}

    createjson := fmt.Sprintf("{\"clusterId\":%d,\"poolId\":%d,\"volumeName\":\"%s\",\"blockThin\":1,\"capacityTotal\":%d,\"redMode\":\"duplication\",\"duplicationNum\":2}", clusterid, poolid, volumename, volsize)
    createpayload := strings.NewReader(createjson)
    gettokenuri := endpoint+"/mng/blockVolume"
    glog.V(4).Infof("new request create volume  %v : %v", gettokenuri, createjson)
    req, err := http.NewRequest("POST", gettokenuri, createpayload)
    if err != nil{
        glog.V(4).Infof("new request create volume  error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr := client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request token error")
        return -1, 0, nil
    }

    req, err = http.NewRequest("GET", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request get volume  error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request get volume error")
        return -1, 0, nil
    }
    if resp.StatusCode != 200 {
        glog.V(4).Infof("response get volume info  status code not 200")
        return -1, 0, nil
    }
    defer resp.Body.Close()
    respbody, _ := ioutil.ReadAll(resp.Body)

    type Volinfo struct {
        VolumeId int `json:"volumeId"`
        ClusterId int `json:"clusterId"`
        VolumeName string `json:"volumeName"`
        PoolId int `json:"poolId"`
        SysUserResourceId int `json:"sysUserResourceId"`
        Status string `json:"status"`
        OperatingStatus string `json:"operatingStatus"`
        OperatingProgress string `json:"operatingProgress"`
        BlockThin int `json:"blockThin"`
        CreateTime string `json:"createTime"`
        Description string `json:"description"`
        CapacityTotal int `json:"capacityTotal"`
        CapacityUsed int `json:"capacityUsed"`
        RedMode string `json:"redMode"`
        DuplicationNum int `json:"duplicationNum"`
        EcTotal int `json:"ecTotal"`
        EcRedundancy int `json:"ecRedundancy"`
        ClusterName string `json:"clusterName"`
        PoolName string `json:"poolName"`
        TargetId int `json:"targetId"`
        VolGroupId int `json:"volGroupId"`
        VolGroupName string `json:"volGroupName"`
        IsOperating bool `json:"isOperating"`
        SysUserId int `json:"sysUserId"`
        MarkDel int `json:"markDel"`
        VolumeNum int `json:"volumeNum"`
        AutoPackSelect bool `json:"autoPackSelect"`
    }

    type ALLVolinfo struct {
        Result string `json:"result"`
        Params []Volinfo `json:"params"`
    }
    var allvinfos ALLVolinfo

    json.Unmarshal([]byte(string(respbody)), &allvinfos)
    for i := 0; i < len(allvinfos.Params); i++ {
        if clusterid == allvinfos.Params[i].ClusterId && poolid == allvinfos.Params[i].PoolId {
            if volumename == allvinfos.Params[i].VolumeName {
                return allvinfos.Params[i].VolumeId, allvinfos.Params[i].TargetId, nil
            }
        }
    }
    return -1, 0, nil
}

func getAndesVol(clusterid int, poolid int, volumename string) (int, int, error){
    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    endpoint := "http://"+storagemngip+":"+storagemngport
    mngtoken := confinfo["mngtoken"]

    client := &http.Client{}


    gettokenuri := endpoint+"/mng/blockVolume"
    req, err := http.NewRequest("GET", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request get volume  error")
        return -1, 0, nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    resp, resperr := client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request get volume error")
        return -1, 0, nil
    }
    if resp.StatusCode != 200 {
        glog.V(4).Infof("response get volume info  status code not 200")
        return -1, 0, nil
    }
    defer resp.Body.Close()
    respbody, _ := ioutil.ReadAll(resp.Body)

    type Volinfo struct {
        VolumeId int `json:"volumeId"`
        ClusterId int `json:"clusterId"`
        VolumeName string `json:"volumeName"`
        PoolId int `json:"poolId"`
        SysUserResourceId int `json:"sysUserResourceId"`
        Status string `json:"status"`
        OperatingStatus string `json:"operatingStatus"`
        OperatingProgress string `json:"operatingProgress"`
        BlockThin int `json:"blockThin"`
        CreateTime string `json:"createTime"`
        Description string `json:"description"`
        CapacityTotal int `json:"capacityTotal"`
        CapacityUsed int `json:"capacityUsed"`
        RedMode string `json:"redMode"`
        DuplicationNum int `json:"duplicationNum"`
        EcTotal int `json:"ecTotal"`
        EcRedundancy int `json:"ecRedundancy"`
        ClusterName string `json:"clusterName"`
        PoolName string `json:"poolName"`
        TargetId int `json:"targetId"`
        VolGroupId int `json:"volGroupId"`
        VolGroupName string `json:"volGroupName"`
        IsOperating bool `json:"isOperating"`
        SysUserId int `json:"sysUserId"`
        MarkDel int `json:"markDel"`
        VolumeNum int `json:"volumeNum"`
        AutoPackSelect bool `json:"autoPackSelect"`
    }

    type ALLVolinfo struct {
        Result string `json:"result"`
        Params []Volinfo `json:"params"`
    }
    var allvinfos ALLVolinfo

    json.Unmarshal([]byte(string(respbody)), &allvinfos)

    vid := 0
    targetid := 0
    for i := 0; i < len(allvinfos.Params); i++ {
        if clusterid == allvinfos.Params[i].ClusterId && poolid == allvinfos.Params[i].PoolId {
            if volumename == allvinfos.Params[i].VolumeName {
                vid = allvinfos.Params[i].VolumeId
                targetid = allvinfos.Params[i].TargetId
            }
        }
    }
    return vid, targetid, nil
}

func updateVol(clusterid int, poolid int, volumename string, volsize int64) (int, int, error){
    vid , targetid, _ := getAndesVol(clusterid,poolid,volumename)

    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    endpoint := "http://"+storagemngip+":"+storagemngport
    mngtoken := confinfo["mngtoken"]
    client := &http.Client{}

    updatevoljson := fmt.Sprintf("{\"volumeId\":%d,\"snapshot\":0,\"alertPercent\":0,\"description\": \"\",\"capacityTotal\":%d,\"redMode\":\"duplication\",\"duplicationNum\":2,\"unitSelect\":\"2\",\"blockThin\":1}", vid, volsize)
    updatepayload := strings.NewReader(updatevoljson)
    gettokenuri := fmt.Sprintf("%s/mng/blockVolume/%d", endpoint, vid)
    glog.V(4).Infof("new request update volume %v", gettokenuri)
    req, _ := http.NewRequest("PUT", gettokenuri, updatepayload)
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    client.Do(req)
    // resp, resperr := client.Do(req)
    return vid, targetid, nil
}

func deleteVol(clusterid int, poolid int, volumename string) (error){
    vid , targetid, _ := getAndesVol(clusterid,poolid,volumename)
    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    endpoint := "http://"+storagemngip+":"+storagemngport
    mngtoken := confinfo["mngtoken"]
    client := &http.Client{}

    gettokenuri := fmt.Sprintf("%s/mng/serTarget/%d/unbind/%d", endpoint, targetid, vid)
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err := http.NewRequest("DELETE", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request create lunid error")
        return nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr := client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create lunid error")
        return nil
    }
    gettokenuri = fmt.Sprintf("%s/mng/serTarget/%d", endpoint, targetid)
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err = http.NewRequest("DELETE", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request create lunid error")
        return nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create lunid error")
        return nil
    }

    gettokenuri = fmt.Sprintf("%s/mng/blockVolume/%d", endpoint, vid)
    glog.V(4).Infof("new request create tid %v", gettokenuri)
    req, err = http.NewRequest("DELETE", gettokenuri, nil)
    if err != nil{
        glog.V(4).Infof("new request create lunid error")
        return nil
    }
    req.Header.Add("X_auth_token", mngtoken)
    req.Header.Add("Content-Type", "application/json")
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request create lunid error")
        return nil
    }

    if resp.StatusCode != 200 {
        glog.V(4).Infof("response storage cluster info  status code not 200")
        return nil
    }
    return nil
}

func getClusterID() (int, int, int, error){
    storageclusterid := -1
    storagepoolid := -1
    serviceclusterid := -1

    if confinfo["csimod"] == "local" {
        glog.V(4).Infof("plugin cfg mod local")
        return 0, 0, 0, nil
    }
    storagemngip := confinfo["storagemngip"]
    storagemngport := confinfo["storagemngport"]
    username := confinfo["storagemngusername"]
    password := confinfo["storagemngpassword"]
    poolname := confinfo["storagemngpoolname"]
    serclustername := confinfo["storagemngsercluster"]

    endpoint := "http://"+storagemngip+":"+storagemngport

    bytepass := []byte(password)
    passmd5 := md5.Sum(bytepass)
    bytepass = []byte(hex.EncodeToString(passmd5[:]))
    passmd5 = md5.Sum(bytepass)
    passwordmd5 := hex.EncodeToString(passmd5[:])

    client := &http.Client{}

    gettokenuri := endpoint+"/mng/j_spring_security_check_kxapi"
    data := url.Values{"password":{passwordmd5},"username":{username}}
    body := strings.NewReader(data.Encode())
    req, err := http.NewRequest("POST", gettokenuri, body)
    if err != nil{
        glog.V(4).Infof("new request error")
        return -1, -1, -1, nil
    }
    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    resp, resperr := client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("request token error")
        return -1, -1, -1, nil
    }
    mngtoken := resp.Header.Get("X_auth_token")
    confinfo["mngtoken"] = mngtoken

    getclusterinfouri := endpoint+"/mng/neuStorCluster?infoType=list&serClusterId=0"
    req, err = http.NewRequest("GET", getclusterinfouri, nil)
    if err != nil{
        glog.V(4).Infof("new request storage cluster info  error")
        return -1, -1, -1, nil
    }
    req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
    req.Header.Add("X_auth_token", mngtoken)
    resp, resperr = client.Do(req)
    if resperr != nil{
        glog.V(4).Infof("response storage cluster info  error")
        return -1, -1, -1, nil
    }
    if resp.StatusCode != 200 {
        glog.V(4).Infof("response storage cluster info  status code not 200")
        return -1, -1, -1, nil
    }
    defer resp.Body.Close()
    respbody, _ := ioutil.ReadAll(resp.Body)

    type Clusterinfo struct {
        ClusterId int `json:"clusterId"`
        ClusterName string `json:"clusterName"`
        Description string `json:"description"`
        DeviceNum int `json:"deviceNum"`
        BlockPoolNum int `json:"blockPoolNum"`
        DfsPoolNum int `json:"dfsPoolNum"`
        ObjPoolNum int `json:"objPoolNum"`
        OperatingStatus string `json:"operatingStatus"`
        HealthStatus string `json:"healthStatus"`
    }

    type ALLClusterinfo struct {
        Result string `json:"result"`
        Message string `json:"message"`
        Params []Clusterinfo `json:"params"`
    }
    var allcinfos ALLClusterinfo

    json.Unmarshal([]byte(string(respbody)), &allcinfos)
    for i := 0; i < len(allcinfos.Params); i++ {
        cid := fmt.Sprintf("%d", allcinfos.Params[i].ClusterId)
        getpoolinfouri := endpoint+"/mng/neuStorCluster/"+cid+"/blockPool?infoType=list"
        req, err = http.NewRequest("GET", getpoolinfouri, nil)
        if err != nil{
            glog.V(4).Infof("new request storage pool info  error")
            return -1, -1, -1, nil
        }
        req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
        req.Header.Add("X_auth_token", mngtoken)
        resp, resperr = client.Do(req)
        if resperr != nil{
            glog.V(4).Infof("response storage pool info  error")
            return -1, -1, -1, nil
        }
        if resp.StatusCode != 200 {
            glog.V(4).Infof("response storage pool info  status code not 200")
            return -1, -1, -1, nil
        }
        defer resp.Body.Close()

        respbody, _ := ioutil.ReadAll(resp.Body)

        type Poolinfo struct {
            BlockPoolId int `json:"blockPoolId"`
            PoolName string `json:"poolName"`
            RedMode string `json:"redMode"`
            CapacityTotal int `json:"capacityTotal"`
            Description string `json:"description"`
            OperatingStatus string `json:"operatingStatus"`
            HealthStatus string `json:"healthStatus"`
        }


        type ALLPoolinfo struct {
            Result string `json:"result"`
            Params []Poolinfo `json:"params"`
        }
        var allpinfos ALLPoolinfo

        json.Unmarshal([]byte(string(respbody)), &allpinfos)
        findpoolid := false
        for j := 0; j < len(allpinfos.Params); j++ {
            if allpinfos.Params[j].PoolName == poolname {
                findpoolid = true
                storageclusterid = allcinfos.Params[i].ClusterId
                storagepoolid = allpinfos.Params[j].BlockPoolId
                //serviceclusterid := -1
                break
            }
        }
        if findpoolid {
            break
        }
    }
    for k := 0; k < len(allcinfos.Params); k++ {
        cid := fmt.Sprintf("%d", allcinfos.Params[k].ClusterId)
        getserclusterinfouri := endpoint+"/mng/sercluster?infoType=list&clusterType=2&storClusterId="+cid
        req, err = http.NewRequest("GET", getserclusterinfouri, nil)
        if err != nil{
            glog.V(4).Infof("new request service cluster info  error")
            return -1, -1, -1, nil
        }
        req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
        req.Header.Add("X_auth_token", mngtoken)
        resp, resperr = client.Do(req)
        if resperr != nil{
            glog.V(4).Infof("response service cluster info  error")
            return -1, -1, -1, nil
        }
        if resp.StatusCode != 200 {
            glog.V(4).Infof("response service cluster info  status code not 200")
            return -1, -1, -1, nil
        }
        defer resp.Body.Close()

        respbody, _ := ioutil.ReadAll(resp.Body)

        type SerClusterinfo struct {
            SerClusterId int `json:"id"`
            ClusterName string `json:"clusterName"`
            ClusterType int `json:"clusterType"`
            StorClusterId int `json:"storClusterId"`
            Description string `json:"description"`
            StorClusterName string `json:"storClusterName"`
            OperatingStatus string `json:"operatingStatus"`
        }


        type ALLSerClusterinfo struct {
            Result string `json:"result"`
            Message string `json:"message"`
            Params []SerClusterinfo `json:"params"`
        }
        var allsinfos ALLSerClusterinfo

        json.Unmarshal([]byte(string(respbody)), &allsinfos)
        findserclusterid := false
        for j := 0; j < len(allsinfos.Params); j++ {
            if allsinfos.Params[j].ClusterName == serclustername {
                findserclusterid = true
                serviceclusterid = allsinfos.Params[j].SerClusterId
                break
            }
        }
        if findserclusterid {
            break
        }
    }

    return storageclusterid, storagepoolid, serviceclusterid, nil

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

func discoverExistingVol() {
    csijsonfile := "/etc/neucli/flexblock_csi_info.json"
    glog.V(4).Infof("discovering existing vol in %s", csijsonfile)

    _, ferr := os.Lstat(csijsonfile)
    if os.IsNotExist(ferr) {
        glog.V(4).Infof("no found configure file %s", csijsonfile)
        return 
    }
    filePtr, err := os.Open(csijsonfile)
    if err != nil {
        fmt.Println("Open file failed [Err:%s]", err.Error())
        return
    }
    defer filePtr.Close()


    decoder := json.NewDecoder(filePtr)
    err = decoder.Decode(&flexBlockVolumes)
    if err != nil {
        fmt.Println("Decoder failed", err.Error())

    } else {
        fmt.Println("Decoder success")
        fmt.Println("load volume ", flexBlockVolumes)
    }
    
}

func saveExistingVol() {
    csijsonfile := "/etc/neucli/flexblock_csi_info.json"
    if ferr := os.MkdirAll(filepath.Dir(csijsonfile), os.FileMode(0755)); ferr != nil {
        glog.V(4).Infof("can not open /etc/neucli/flexblock_csi_info.json file: %v", ferr)
        return
    }
    csiinfofile, fileerr := os.OpenFile(csijsonfile, syscall.O_RDWR|syscall.O_CREAT|syscall.O_TRUNC, 0666)
    if fileerr != nil {
        glog.V(4).Infof("can not open /etc/neucli/flexblock_csi_info.json file: %v", fileerr)
        return
    }
    defer csiinfofile.Close() // in case we fail before the explicit close

    encoder := json.NewEncoder(csiinfofile)
    jsonerr := encoder.Encode(flexBlockVolumes)
    if jsonerr != nil {
        glog.V(4).Infof("Encoder failed", jsonerr)
    }

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
        }
    }
}

func (hp *flexBlock) Run() {
    // Create GRPC servers
    hp.ids = NewIdentityServer(hp.name, hp.version)
    hp.ns = NewNodeServer(hp.nodeID, hp.ephemeral, hp.maxVolumesPerNode)
    hp.cs = NewControllerServer(hp.ephemeral, hp.nodeID)

    discoverExistingVol()
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
    diskpath := "/dev"

    storclusterid, storpoolid, sercluseterid, getclustererr := getClusterID()
    if getclustererr != nil {
            return nil, status.Errorf(codes.Internal, "failed get cluster info %v", getclustererr)
    }
    var cmd []string
    executor := utilexec.New()

    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flex-%s", volID)
    if storclusterid == 0 {
        size := fmt.Sprintf("%dM", cap/mib)
        cmd = []string{"xioadm", "vdi", "create", volID, size}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil && !strings.Contains(string(out), "VDI exists already") {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol %v: %v", err, string(out))
        }

        iqnname = fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)
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
        diskpath =  fmt.Sprintf("/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-%s-lun-1", iqnname)
    } else {
        glog.V(4).Infof("find cluster id : %v,%v,%v", storclusterid, storpoolid, sercluseterid )
        //volsize := cap*1024*1024
        vid, targetid, createerr := createVol(storclusterid, storpoolid, volID, cap)
        if createerr != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol create from storage cluster:%d. %v ", storclusterid, createerr)
        }
        if targetid == 0 {
            tid, lunid, tiderr := createTarget(storclusterid, sercluseterid, volID, vid)
            if tiderr != nil {
                return nil, status.Errorf(codes.Internal, "failed createflexblockvol create from service cluster:%d. %v ", storclusterid, createerr)
            }
            glog.V(4).Infof("create volume in tgt tid:lunid : %v:%v", tid, lunid)
        }
        storageiscsiips := confinfo["storageiscsiips"]
        storiplist := strings.Split(storageiscsiips, ",")
        for i := 0; i < len(storiplist); i++ {
            cmd1 := []string{"iscsiadm", "-m", "discovery", "-t", "st", "-p", storiplist[i]}
            glog.V(4).Infof("Command Start: %v", cmd1)
            out1, err1 := executor.Command(cmd1[0], cmd1[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out1))
            if err1 != nil {
                return nil, status.Errorf(codes.Internal, "failed createflexblockvol for discovery target error %v: %v", err1, string(out1))
            }

            cmd1 = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-p", storiplist[i], "-l"}
            glog.V(4).Infof("Command Start: %v", cmd1)
            out1, err1 = executor.Command(cmd1[0], cmd1[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out1))
            if err1 != nil {
                return nil, status.Errorf(codes.Internal, "failed createflexblockvol for login target error %v: %v", err1, string(out1))
            }
        }

        iscsifiles, iscsierr := ioutil.ReadDir("/dev/disk/by-path/")
        if iscsierr != nil {
            glog.V(4).Infof("read dir /dev/disk/by-path/ error %v", iscsierr)
        }

        for _, iscsifile := range iscsifiles {
            iscsipathpre :=  fmt.Sprintf("ip-%s:3260-iscsi-%s-lun-", storiplist[0], iqnname)
            if strings.HasPrefix(iscsifile.Name(), iscsipathpre) {
                diskpath =  fmt.Sprintf("/dev/disk/by-path/%s", iscsifile.Name())
                break
            }
        }
        diskname, _ := os.Readlink(diskpath)
        diskname = diskname[strings.LastIndex(diskname, "/")+1:]
        sysblkdir := fmt.Sprintf("/sys/block/%s/holders/", diskname)
        holderfiles, holdererr := ioutil.ReadDir(sysblkdir)
        if holdererr != nil {
            glog.V(4).Infof("read dir /sys/block holders error %v", holdererr)
        }
        if len(holderfiles) == 0 && len(storiplist) > 1 {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for get pool ")
        }
        if len(holderfiles) > 0 {
            diskpath = fmt.Sprintf("/dev/%s", holderfiles[0].Name())
        }
    }



    switch volAccessType {
    case mountAccess:
        err := os.MkdirAll(path, 0777)
        if err != nil {
            return nil, err
        }

        time.Sleep(time.Duration(5)*time.Second)
        cmd = []string{"mkfs.ext4", diskpath, "-F"}
        glog.V(4).Infof("Command Start: %v", cmd)
        cmdout, cmderr := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(cmdout))
        if cmderr != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for mkfs ext4 error %v: %v", err, string(cmdout))
        }

        cmd = []string{"mount", "-t", "ext4", diskpath, path}
        glog.V(4).Infof("Command Start: %v", cmd)
        cmdout, cmderr = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(cmdout))
        if cmderr != nil {
            return nil, status.Errorf(codes.Internal, "failed createflexblockvol for mount target error %v: %v", err, string(cmdout))
        }

    case blockAccess:
        // executor := utilexec.New()
        // // size := fmt.Sprintf("%dM", cap/mib)
        // // Create a block file.
        // _, err := os.Stat(path)
        // if err != nil {
        //     if os.IsNotExist(err) {
        //         cmdout, cmderr := executor.Command("fallocate", "-l", size, path).CombinedOutput()
        //         if cmderr != nil {
        //             return nil, fmt.Errorf("failed to create block device: %v, %v", err, string(cmdout))
        //         }
        //     } else {
        //         return nil, fmt.Errorf("failed to stat block device: %v, %v", path, cmderr)
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

    saveExistingVol()

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
    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flex-%s", volID)

    storclusterid, storpoolid, sercluseterid, getclustererr := getClusterID()
    if getclustererr != nil {
            return status.Errorf(codes.Internal, "failed get cluster info %v", getclustererr)
    }

    diskpath := "/dev/"
    if storclusterid == 0 {
        iqnname = fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)
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

        diskpath =  fmt.Sprintf("/dev/disk/by-path/ip-127.0.0.1:3260-iscsi-%s-lun-1", iqnname)
    } else {
        glog.V(4).Infof("find cluster id : %v,%v,%v", storclusterid, storpoolid, sercluseterid )
        //vid, targetid, createerr := updateVol(storclusterid, storpoolid, volID, volume.VolSize)
        _, _, updateerr := updateVol(storclusterid, storpoolid, volID, volume.VolSize)
        if updateerr != nil {
            return status.Errorf(codes.Internal, "failed updateflexblockvol update target %v", updateerr)
        }

        cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-R"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return status.Errorf(codes.Internal, "failed updateflexblockvol update target %v: %v", err, string(out))
        }

        iscsifiles, iscsierr := ioutil.ReadDir("/dev/disk/by-path/")
        if iscsierr != nil {
            glog.V(4).Infof("read dir /dev/disk/by-path/ error %v", iscsierr)
        }

        storageiscsiips := confinfo["storageiscsiips"]
        storiplist := strings.Split(storageiscsiips, ",")
        for _, iscsifile := range iscsifiles {
            iscsipathpre :=  fmt.Sprintf("ip-%s:3260-iscsi-%s-lun-", storiplist[0], iqnname)
            if strings.HasPrefix(iscsifile.Name(), iscsipathpre) {
                diskpath =  fmt.Sprintf("/dev/disk/by-path/%s", iscsifile.Name())
                break
            }
        }
        diskname, _ := os.Readlink(diskpath)
        diskname = diskname[strings.LastIndex(diskname, "/")+1:]
        sysblkdir := fmt.Sprintf("/sys/block/%s/holders/", diskname)
        holderfiles, holdererr := ioutil.ReadDir(sysblkdir)
        if holdererr != nil {
            glog.V(4).Infof("read dir /sys/block holders error %v", holdererr)
        }
        if len(holderfiles) == 0 && len(storiplist) > 1 {
            return status.Errorf(codes.Internal, "failed createflexblockvol for get pool ")
        }
        if len(holderfiles) > 0 {
            diskpath = fmt.Sprintf("/dev/%s", holderfiles[0].Name())
            cmd = []string{"multipath", "-r", diskpath}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, err = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
            diskpath = fmt.Sprintf("/dev/%s", holderfiles[0].Name())
        }

    }

    if vol.VolAccessType == mountAccess {
        cmd = []string{"resize2fs", diskpath}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, err := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))
        if err != nil {
            return status.Errorf(codes.Internal, "failed updateflexblockvol resize fs target %v: %v", err, string(out))
        }
    }

    flexBlockVolumes[volID] = volume

    saveExistingVol()

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

    iqnname := fmt.Sprintf("iqn.2017-10-30.kx.flex-%s", volID)

    storclusterid, storpoolid, sercluseterid, getclustererr := getClusterID()
    if getclustererr != nil {
            return status.Errorf(codes.Internal, "failed get cluster info %v", getclustererr)
    }

    if storclusterid == 0 {
        iqnname = fmt.Sprintf("iqn.2017-10-30.kx.flexcsi-%s", volID)
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
    } else {
        storageiscsiips := confinfo["storageiscsiips"]
        storiplist := strings.Split(storageiscsiips, ",")
        diskpath := "/dev/"

        iscsifiles, iscsierr := ioutil.ReadDir("/dev/disk/by-path/")
        if iscsierr != nil {
            glog.V(4).Infof("read dir /dev/disk/by-path/ error %v", iscsierr)
        }

        for _, iscsifile := range iscsifiles {
            iscsipathpre :=  fmt.Sprintf("ip-%s:3260-iscsi-%s-lun-", storiplist[0], iqnname)
            if strings.HasPrefix(iscsifile.Name(), iscsipathpre) {
                diskpath =  fmt.Sprintf("/dev/disk/by-path/%s", iscsifile.Name())
                break
            }
        }
        diskname, _ := os.Readlink(diskpath)
        diskname = diskname[strings.LastIndex(diskname, "/")+1:]

        sysblkdir := fmt.Sprintf("/sys/block/%s/holders/", diskname)
        holderfiles, holdererr := ioutil.ReadDir(sysblkdir)
        if holdererr != nil {
            glog.V(4).Infof("read dir /sys/block holders error %v", holdererr)
        }
        if len(holderfiles) > 0 {
            dmname := holderfiles[0].Name()
            mapperfiles, mappererr := ioutil.ReadDir("/dev/mapper/")
            if mappererr != nil {
                glog.V(4).Infof("read dir /dev/mapper/ error %v", mappererr)
            }

            for _, mapperfile := range mapperfiles {
                mapperpath := fmt.Sprintf("/dev/mapper/%s", mapperfile.Name())
                glog.V(4).Infof("find device mapper: %v", mapperpath)
                mdmname, mdmerr := os.Readlink(mapperpath)
                if mdmerr != nil {
                    continue
                }
                mdmname1 := mdmname[strings.LastIndex(mdmname, "/")+1:]
                glog.V(4).Infof("link device mapper: %v:%v compare with %v", mapperpath, mdmname1, dmname)
                if dmname == mdmname1 {
                    cmd = []string{"dmsetup", "remove", mapperfile.Name(), "--force"}
                    glog.V(4).Infof("Command Start: %v", cmd)
                    out, _ := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
                    glog.V(4).Infof("Command Finish: %v", string(out))
                }
            }
        }

        cmd = []string{"iscsiadm", "-m", "node", "-T", iqnname, "-u"}
        glog.V(4).Infof("Command Start: %v", cmd)
        out, _ := executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
        glog.V(4).Infof("Command Finish: %v", string(out))


        for i := 0; i < len(storiplist); i++ {
            cmd = []string{"iscsiadm", "-m", "discoverydb", "-n", iqnname, "-o", "delete", "-t", "st", "-p", storiplist[i]}
            glog.V(4).Infof("Command Start: %v", cmd)
            out, _ = executor.Command(cmd[0], cmd[1:]...).CombinedOutput()
            glog.V(4).Infof("Command Finish: %v", string(out))
        }

        glog.V(4).Infof("find cluster id : %v,%v,%v", storclusterid, storpoolid, sercluseterid )
        //vid, targetid, createerr := updateVol(storclusterid, storpoolid, volID, volume.VolSize)
        updateerr := deleteVol(storclusterid, storpoolid, volID)
        if updateerr != nil {
            return status.Errorf(codes.Internal, "failed updateflexblockvol update target %v", updateerr)
        }
    }

    if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
        return err
    }
    delete(flexBlockVolumes, volID)

    saveExistingVol()

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
