# CSI Driver for Kaixiang MegaBric


**Repository for CSI Driver for Kaixiang MegaBric **

## Overview
CSI Driver for Kaixiang MegaBric is a Container Storage Interface (CSI) driver that provides support for provisioning persistent storage using Kaixiang MegaBric storage. 

## Building
This project is a Go module (see golang.org Module information for explanation). 
The dependencies for this project are in the go.mod file.

To build the source, execute `build.sh` to build rpm or deb package file

## Driver Installation
### Install RPM
Install Kaixiang MegaBric CSI RPM Package 
### Create Storage Pool
Create Storage Pool on Kaixiang MegaBric Management web 
### Edit CSI configure 
Edit /etc/neucli/flexblock_plugin.cfg 
```
storagemngip=192.168.108.217
storagemngport=8060
storagemngusername=admin
storagemngpassword=password
storagemngclustername=clustername
storagemngpoolname=poolname
```
### Deployment CSI Driver 
deployment K8S yaml 
```
kubectl create -f /usr/share/doc/flexblockplugin/kubernetes/flexblock
```


### Using driver
Create  PVC use csi-flexblock-sc
```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-csi-flexblock-pvc
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: csi-flexblock-sc
```

