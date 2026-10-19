GO ?= go

.PHONY: all linux windows windows-gui run test clean
all: linux windows windows-gui
linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o dist/agent-viewer-linux ./cmd/agent-viewer
windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o dist/agent-viewer-windows.exe ./cmd/agent-viewer
# no-console build: starts the embedded window by default, logs to %LOCALAPPDATA%\agent-viewer\agent-viewer.log
windows-gui:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "-H=windowsgui -X main.defaultGUI=1" -o dist/agent-viewer-gui.exe ./cmd/agent-viewer
run: linux
	./dist/agent-viewer-linux
test:
	$(GO) vet ./... && $(GO) test ./...
clean:
	rm -rf dist
