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

package main

import (
    "flag"
    "fmt"
    "os"
    "path"
    "path/filepath"
    "syscall"

    "192.168.108.165/kubernetes-csi/csi-driver-flexblock/pkg/flexblock"
)

func init() {
    flag.Set("logtostderr", "false")
    flag.Set("log_dir", "/var/log/flexblockplugin")
}

var (
    endpoint          = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
    driverName        = flag.String("drivername", "flexblock.csi.k8s.io", "name of the driver")
    nodeID            = flag.String("nodeid", "", "node id")
    ephemeral         = flag.Bool("ephemeral", false, "publish volumes in ephemeral mode even if kubelet did not ask for it (only needed for Kubernetes 1.15)")
    maxVolumesPerNode = flag.Int64("maxvolumespernode", 0, "limit of volumes per node")
    showVersion       = flag.Bool("version", false, "Show version.")
    // Set by the build process
    version = ""
)

func main() {
    flag.Parse()
    pidfile := "/var/run/flexblockplugin.pid"
    if err := os.MkdirAll(filepath.Dir(pidfile), os.FileMode(0755)); err != nil {
        return 
    }
    
    file, err := os.OpenFile(pidfile, syscall.O_RDWR|syscall.O_CREAT|syscall.O_TRUNC, 0666)
    if err != nil {
        return 
    }
    defer file.Close() // in case we fail before the explicit close
    
    _, err = fmt.Fprintf(file, "%d", os.Getpid())
    if err != nil {
        return 
    }
    
    err = file.Close()
    if err != nil {
        return 
    }


    if *showVersion {
        baseName := path.Base(os.Args[0])
        fmt.Println(baseName, version)
        return
    }

    if *ephemeral {
        fmt.Fprintln(os.Stderr, "Deprecation warning: The ephemeral flag is deprecated and should only be used when deploying on Kubernetes 1.15. It will be removed in the future.")
    }

    handle()
    os.Exit(0)
}

func handle() {
    driver, err := flexblock.NewFlexBlockDriver(*driverName, *nodeID, *endpoint, *ephemeral, *maxVolumesPerNode, version)
    if err != nil {
        fmt.Printf("Failed to initialize driver: %s", err.Error())
        os.Exit(1)
    }
    driver.Run()
}
