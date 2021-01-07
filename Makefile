VERSION=21.01.01.1
GIT_VER=$(shell git rev-list HEAD| wc -l)
GIT_COMMIT=$(shell git rev-parse --short HEAD)

all:
	go build -a -ldflags ' -X main.version=${VERSION}_${GIT_COMMIT}_${GIT_VER}  -extldflags "-static"' -o ./bin/flexblockplugin ./cmd/flexblockplugin
clean:
	rm -fr ./bin/
install:
	mkdir -p $(DESTDIR)/var/log/flexblockplugin/
	mkdir -p ${DESTDIR}/var/lib/kubelet/plugins/csi-flexblock/
	mkdir -p ${DESTDIR}/usr/sbin/
	mkdir -p ${DESTDIR}/lib/systemd/system/
	mkdir -p ${DESTDIR}/usr/share/doc/flexblockplugin/
	mkdir -p ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/
	install -m 755 bin/flexblockplugin $(DESTDIR)/usr/sbin/
	install -m 644 systemctl/flexblockplugin.service $(DESTDIR)/lib/systemd/system/
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-attacher.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-attacher.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-driverinfo.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-driverinfo.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-plugin.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-plugin.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-provisioner.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-provisioner.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-resizer.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-resizer.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-testing.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-testing.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-storageclass.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-storageclass.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-attacher-rbac.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-attacher-rbac.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-provisioner-rbac.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-provisioner-rbac.yaml
	install -m 644 deploy/kubernetes/flexblock/csi-flexblock-resizer-rbac.yaml ${DESTDIR}/usr/share/doc/flexblockplugin/kubernetes/flexblock/csi-flexblock-resizer-rbac.yaml
	systemctl daemon-reload
uninstall:
	rm -f /usr/sbin/flexblockplugin
	rm -f /lib/systemd/system/flexblockplugin.service
	systemctl daemon-reload


PHONY: all clean install uninstall

