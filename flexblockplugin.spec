Name:   flexblockplugin
Version:        %{VERSION}_%{CUR_BR}_%{GIT_COMMIT}
Release:        %{?GIT_VER}
Summary:        Kaixiangtech FlexBlock k8s csi plugin

Group:  System Environment/Base
License:        GPL


%description
Kaixiangtech FlexBlock k8s csi plugin



%install
make install DESTDIR=%{buildroot}


%files
/usr/sbin/flexblockplugin
/lib/systemd/system/flexblockplugin.service
/var/log/flexblockplugin/
/var/lib/kubelet/plugins/csi-flexblock/
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-attacher.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-driverinfo.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-plugin.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-provisioner.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-resizer.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-testing.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-storageclass.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-attacher-rbac.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-provisioner-rbac.yaml
/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-resizer-rbac.yaml
%doc


%post
systemctl daemon-reload


%changelog

