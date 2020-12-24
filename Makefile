all:
	go build -a -ldflags ' -X main.version=v1.0.0.0  -extldflags "-static"' -o ./bin/flexblockplugin ./cmd/flexblockplugin
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
	$(shell systemctl daemon-reload)
uninstall:
	rm -f /usr/sbin/flexblockplugin
	rm -f /lib/systemd/system/flexblockplugin.service
	$(shell systemctl daemon-reload)


PHONY: all clean install uninstall

