all:
	go build -a -ldflags ' -X main.version=v1.0.0.0  -extldflags "-static"' -o ./bin/flexblockplugin ./cmd/flexblockplugin
clean:
	rm -fr ./bin/
install:
	mkdir -p $(DESTDIR)/var/log/flexblockplugin/
	mkdir -p ${DESTDIR}/var/lib/kubelet/plugins/csi-flexblock/
	mkdir -p ${DESTDIR}/usr/sbin/
	mkdir -p ${DESTDIR}/lib/systemd/system/
	install -m 755 bin/flexblockplugin $(DESTDIR)/usr/sbin/
	install -m 644 systemctl/flexblockplugin.service $(DESTDIR)/lib/systemd/system/
	$(shell systemctl daemon-reload)
uninstall:
	rm -f /usr/sbin/flexblockplugin
	rm -f /lib/systemd/system/flexblockplugin.service
	$(shell systemctl daemon-reload)


PHONY: all clean install uninstall

