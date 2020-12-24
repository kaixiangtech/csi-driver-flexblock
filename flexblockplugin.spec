Name:   flexblock
Version:        %{VERSION}_%{CUR_BR}_%{GIT_COMMIT}
Release:        1%{?GIT_VER}
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
%doc



%changelog

