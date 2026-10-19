GO ?= go

.PHONY: all linux windows run clean
all: linux windows
linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o dist/agent-viewer-linux .
windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o dist/agent-viewer-windows.exe .
run: linux
	./dist/agent-viewer-linux
clean:
	rm -rf dist agent-viewer
