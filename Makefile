GOCMD=go
PLUGINS=plugins/
OUTFILE=webircgateway

GO_VERSION=$(word 3, $(shell go version))
GIT_COMMIT=$(shell git rev-list -1 HEAD)

LDFLAGS=-ldflags "-X main.GITCOMMIT=$(GIT_COMMIT) -X main.BUILTWITHGO=$(GO_VERSION)"

build-all: build-plugins build

build:
	$(GOCMD) build $(LDFLAGS) -o $(OUTFILE) -v main.go

build-crosscompile:
	GOOS=linux GOARCH=amd64 $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_linux_amd64 -v main.go
	GOOS=linux GOARCH=arm64 $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_linux_arm64 -v main.go
	GOOS=darwin GOARCH=amd64 $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_darwin_amd64 -v main.go
	GOOS=windows GOARCH=amd64 $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_window_amd64 -v main.go
	GOOS=freebsd GOARCH=amd64 $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_bsd_amd64 -v main.go
	GOOS=freebsd GOARCH=arm $(GOCMD) build $(LDFLAGS) -o $(OUTFILE)_bsd_arm -v main.go

build-plugins:
	@for plugin in $(sort $(dir $(wildcard plugins/*/*.go))); do \
		plugin_name=$$plugin; \
		export plugin_name; \
		plugin_name=$$(echo $$plugin_name | cut -d'/' -f2); \
		echo Building $$plugin; \
		$(GOCMD) build -buildmode=plugin -v -o "plugins/$$plugin_name.so" plugins/$$plugin_name/*; \
	done

run:
	$(GOCMD) run main.go

run-proxy:
	$(GOCMD) run main.go -run=proxy

build-docker:
	docker run --rm -v "$$PWD":/myapp -w /myapp golang:1.13.4 make
	rm -rf ./dockerbuild
	mkdir -p ./dockerbuild/plugins
	mv webircgateway ./dockerbuild/kiwiserver
	mv plugins/*.so ./dockerbuild/plugins/

