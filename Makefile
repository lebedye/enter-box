BINARY   := enter-box
PKG      := ./cmd/enter-box
ARCH     ?= arm64
CONFIG   ?= /etc/enter-box/enter-box.yaml
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
# Clash API управляет выбором узлов, uTLS нужен REALITY, QUIC для Hysteria2.
TAGS     := with_clash_api,with_utls,with_quic

# Имя выходного файла совпадает с тем, что ждёт deploy/deploy.sh.
BIN_ARM64 := dist/$(BINARY)-linux-$(ARCH)
BIN_HOST  := dist/$(BINARY)

SERVICE   := deploy/systemd/$(BINARY).service
TEMPLATE  := deploy/enter-box.router.yaml

.PHONY: all build build-arm64 build-host tidy fmt vet test run dump \
        install setup check uninstall release clean sha256

all: build

build-arm64:
	mkdir -p dist
	GOOS=linux GOARCH=$(ARCH) CGO_ENABLED=0 \
		go build -tags $(TAGS) -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BIN_ARM64) $(PKG)
	@echo "built: $(BIN_ARM64)"
	@sha256sum $(BIN_ARM64)

build-host:
	mkdir -p dist
	go build -tags $(TAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_HOST) $(PKG)
	@echo "built: $(BIN_HOST)"

build: build-arm64

tidy:
	go mod tidy

fmt:
	gofmt -s -w .

vet:
	go vet -tags $(TAGS) ./...

test:
	go test -tags $(TAGS) ./...

run:
	go run -tags $(TAGS) $(PKG) run -c $(CONFIG)

dump:
	go run -tags $(TAGS) $(PKG) run -c $(CONFIG) -dump

# Dev-путь для локальной проверки. Production-путь — deploy/install.sh:
# он делает бэкапы, откат, настройку NetworkManager и restart с проверкой.
install:
	@if [ "$$(id -u)" -ne 0 ]; then \
		echo "нужен root: sudo make install"; exit 1; fi
	install -Dm0755 $(BIN_ARM64) /usr/local/bin/$(BINARY)
	install -Dm0644 $(SERVICE) /etc/systemd/system/$(BINARY).service
	install -d /etc/$(BINARY)
	[ -f /etc/$(BINARY)/$(BINARY).yaml ] || \
		install -m0600 $(TEMPLATE) /etc/$(BINARY)/$(BINARY).yaml
	systemctl daemon-reload
	@echo "installed. Run: sudo $(BINARY) setup"

setup:
	$(BINARY) setup

check:
	$(BINARY) check

uninstall:
	-systemctl stop $(BINARY)
	-systemctl disable $(BINARY)
	-rm -f /etc/systemd/system/$(BINARY).service
	-systemctl daemon-reload
	-rm -f /usr/local/bin/$(BINARY)
	@echo "removed. Config /etc/$(BINARY)/ kept."

release: build-arm64
	mkdir -p dist/release
	cp $(BIN_ARM64) dist/release/$(BINARY)
	cp $(TEMPLATE) dist/release/$(BINARY).example.yaml
	cp $(SERVICE) dist/release/$(BINARY).service
	cp README.md dist/release/README.md
	cp docs/INSTALL.md dist/release/INSTALL.md 2>/dev/null || true
	cp docs/OPERATIONS.md dist/release/OPERATIONS.md 2>/dev/null || true
	tar czf dist/$(BINARY)-$(VERSION)-linux-$(ARCH).tar.gz -C dist/release .
	rm -rf dist/release
	@echo "built: dist/$(BINARY)-$(VERSION)-linux-$(ARCH).tar.gz"

sha256:
	@sha256sum $(BIN_ARM64) 2>/dev/null || echo "no $(BIN_ARM64), run: make build-arm64"

clean:
	rm -rf dist