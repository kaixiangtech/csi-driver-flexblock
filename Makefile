all:
	go build -a -ldflags ' -X main.version=v1.0.0.0  -extldflags "-static"' -o ./bin/flexblockplugin ./cmd/flexblockplugin
clean:
	rm -fr ./bin/

PHONY: all clean
