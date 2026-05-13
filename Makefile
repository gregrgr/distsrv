BINARY := distsrv
CLI    := distsrv-cli
PKG    := ./cmd/distsrv
CLI_PKG := ./cmd/distsrv-cli
LDFLAGS := -s -w
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
DIST_DIR := dist

.PHONY: all build build-linux build-linux-arm64 \
        build-cli build-cli-mac build-cli-all \
        release release-amd64 release-arm64 \
        release-mac release-mac-arm64 release-mac-amd64 \
        release-cli release-cli-linux-amd64 release-cli-linux-arm64 release-cli-windows-amd64 \
        release-all checksums \
        tidy clean run dev fmt vet test

all: build

build:
	go build -ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='$(LDFLAGS)' -o $(BINARY) $(PKG)

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags='$(LDFLAGS)' -o $(BINARY)-arm64 $(PKG)

# ============ CLI (single binary) ============

build-cli:
	go build -ldflags='$(LDFLAGS)' -o $(CLI) $(CLI_PKG)

build-cli-mac:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags='$(LDFLAGS)' -o $(DIST_DIR)/$(CLI)-darwin-arm64 $(CLI_PKG)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags='$(LDFLAGS)' -o $(DIST_DIR)/$(CLI)-darwin-amd64 $(CLI_PKG)
	@ls -lh $(DIST_DIR)/$(CLI)-darwin-*

build-cli-all: build-cli-mac
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags='$(LDFLAGS)' -o $(DIST_DIR)/$(CLI)-linux-amd64  $(CLI_PKG)
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -ldflags='$(LDFLAGS)' -o $(DIST_DIR)/$(CLI)-linux-arm64  $(CLI_PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags='$(LDFLAGS)' -o $(DIST_DIR)/$(CLI)-windows-amd64.exe $(CLI_PKG)
	@ls -lh $(DIST_DIR)/$(CLI)-*

# ============ Server release tarballs ============

release: release-amd64 release-arm64
	@ls -lh $(DIST_DIR)/distsrv-*-linux-*.tar.gz

release-amd64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle ARCH=amd64

release-arm64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle ARCH=arm64

.PHONY: _bundle
_bundle:
	@stage=$$(mktemp -d) && \
	echo "==> building $(BINARY) for linux/$(ARCH)" && \
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) \
	  go build -ldflags='$(LDFLAGS)' -o $$stage/$(BINARY) $(PKG) && \
	cp deploy.sh distsrv.service config.example.toml README.md $$stage/ && \
	chmod +x $$stage/deploy.sh && \
	tarball=$(DIST_DIR)/$(BINARY)-$(VERSION)-linux-$(ARCH).tar.gz && \
	tar -czf $$tarball -C $$stage . && \
	rm -rf $$stage && \
	echo "==> wrote $$tarball ($$(du -h $$tarball | cut -f1))"

# ============ CLI release tarballs ============
# darwin: with mac/* (install.sh, xcode-archive-upload.sh, exportOptions, README)
# linux/windows: cli binary + minimal README

release-mac: release-mac-arm64 release-mac-amd64
	@ls -lh $(DIST_DIR)/$(CLI)-*-darwin-*.tar.gz

release-mac-arm64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle_cli_mac OS=darwin ARCH=arm64

release-mac-amd64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle_cli_mac OS=darwin ARCH=amd64

.PHONY: _bundle_cli_mac
_bundle_cli_mac:
	@stage=$$(mktemp -d) && \
	echo "==> building $(CLI) for $(OS)/$(ARCH)" && \
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) \
	  go build -ldflags='$(LDFLAGS)' -o $$stage/$(CLI) $(CLI_PKG) && \
	cp mac/install.sh mac/xcode-archive-upload.sh mac/exportOptions-adhoc.plist mac/README.md $$stage/ && \
	chmod +x $$stage/install.sh $$stage/xcode-archive-upload.sh && \
	tarball=$(DIST_DIR)/$(CLI)-$(VERSION)-$(OS)-$(ARCH).tar.gz && \
	tar -czf $$tarball -C $$stage . && \
	rm -rf $$stage && \
	echo "==> wrote $$tarball ($$(du -h $$tarball | cut -f1))"

release-cli: release-mac release-cli-linux-amd64 release-cli-linux-arm64 release-cli-windows-amd64
	@ls -lh $(DIST_DIR)/$(CLI)-*.tar.gz

release-cli-linux-amd64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle_cli_minimal OS=linux ARCH=amd64 EXT=

release-cli-linux-arm64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle_cli_minimal OS=linux ARCH=arm64 EXT=

release-cli-windows-amd64:
	@mkdir -p $(DIST_DIR)
	$(MAKE) _bundle_cli_minimal OS=windows ARCH=amd64 EXT=.exe

.PHONY: _bundle_cli_minimal
_bundle_cli_minimal:
	@stage=$$(mktemp -d) && \
	echo "==> building $(CLI) for $(OS)/$(ARCH)" && \
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) \
	  go build -ldflags='$(LDFLAGS)' -o $$stage/$(CLI)$(EXT) $(CLI_PKG) && \
	cp mac/README.md $$stage/README.md && \
	tarball=$(DIST_DIR)/$(CLI)-$(VERSION)-$(OS)-$(ARCH).tar.gz && \
	tar -czf $$tarball -C $$stage . && \
	rm -rf $$stage && \
	echo "==> wrote $$tarball ($$(du -h $$tarball | cut -f1))"

# ============ All-in-one ============

release-all: release release-cli checksums

# Generate a single SHA256 manifest for all tarballs so users can verify downloads
checksums:
	@cd $(DIST_DIR) && sha256sum *.tar.gz > SHA256SUMS.txt && \
	echo "==> wrote $(DIST_DIR)/SHA256SUMS.txt" && \
	cat SHA256SUMS.txt

# ============ Misc ============

tidy:
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

dev: build
	./$(BINARY) -config ./config.dev.toml

clean:
	rm -f $(BINARY) $(BINARY)-arm64 $(BINARY).exe $(CLI) $(CLI).exe
	rm -rf $(DIST_DIR)
