GO ?= go

.PHONY: all linux windows windows-gui run clean
all: linux windows windows-gui
linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o dist/agent-viewer-linux .
windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o dist/agent-viewer-windows.exe .
# no-console build: starts the embedded window by default, logs to %LOCALAPPDATA%\agent-viewer\agent-viewer.log
windows-gui:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "-H=windowsgui -X main.defaultGUI=1" -o dist/agent-viewer-gui.exe .
run: linux
	./dist/agent-viewer-linux
clean:
	rm -rf dist agent-viewer
