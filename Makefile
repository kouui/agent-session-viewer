GO ?= go

.PHONY: build windows all run clean
build:
	$(GO) build -o agent-viewer .
windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o dist/agent-viewer.exe .
all: build windows
run: build
	./agent-viewer
clean:
	rm -rf agent-viewer dist
