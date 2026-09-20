VERSION ?= 0.1.3
GO ?= go
PLUGIN_ID = freebuff-cli
OUT = dist/$(PLUGIN_ID)-v$(VERSION).so
ARCHIVE = dist/$(PLUGIN_ID)_$(VERSION)_linux_amd64.zip

.PHONY: build test vet fmt clean

# The plugin is a linux/amd64 c-shared object, so building is refused on any other
# target rather than producing a library the gateway cannot load.
build:
	test "$$(go env GOOS)" = "linux"
	test "$$(go env GOARCH)" = "amd64"
	mkdir -p dist
	CGO_ENABLED=1 $(GO) build -buildvcs=false -trimpath -buildmode=c-shared \
		-ldflags "-s -w -X main.pluginVersion=$(VERSION)" -o "$(OUT)" ./src
	rm -f dist/*.h

test:
	$(GO) vet ./...
	$(GO) test ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./src

clean:
	rm -rf dist
