BINARY  := enter-box
PKG     := ./cmd/enter-box
ARCH    ?= arm64
CONFIG  ?= /etc/enter-box/enter-box.yaml
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build build-arm64 build-host tidy fmt vet test run dump \
        install setup check uninstall release clean

all: build

build-arm64:
	mkdir -p dist
	GOOS=linux GOARCH=$(ARCH) CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o dist/$(BINARY)-$(ARCH) $(PKG)
	@echo "→ dist/$(BINARY)-$(ARCH)"

build-host:
	mkdir -p dist
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) $(PKG)
	@echo "→ dist/$(BINARY)"

build: build-arm64

tidy:
	go mod tidy

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test ./...

run:
	go run $(PKG) run -c $(CONFIG) -v

dump:
	go run $(PKG) run -c $(CONFIG) -dump

install:
	@if [ "$$(id -u)" -ne 0 ]; then \
		echo "нужен root: sudo make install"; exit 1; fi
	install -Dm0755 dist/$(BINARY)-$(ARCH) /usr/local/bin/$(BINARY)
	install -Dm0644 deploy/systemd/$(BINARY).service /etc/systemd/system/$(BINARY).service
	install -d /etc/$(BINARY)
	[ -f /etc/$(BINARY)/$(BINARY).yaml ] || \
		install -m0644 $(BINARY).example.yaml /etc/$(BINARY)/$(BINARY).yaml
	systemctl daemon-reload
	@echo "✓ установлено. Запустите: sudo $(BINARY) setup"

setup:
	$(BINARY) setup

check:
	$(BINARY) check

uninstall:
	-systemctl stop $(BINARY)-lan-addr 2>/dev/null
	-systemctl disable $(BINARY)-lan-addr 2>/dev/null
	-rm -f /etc/systemd/system/$(BINARY)-lan-addr.service
	-rm -f /usr/local/bin/$(BINARY)-lan-addr
	-systemctl stop $(BINARY)
	-systemctl disable $(BINARY)
	-rm -f /etc/systemd/system/$(BINARY).service
	-systemctl daemon-reload
	-rm -f /usr/local/bin/$(BINARY)
	@echo "✓ удалено. Конфиг /etc/$(BINARY)/ оставлен."

release: build-arm64
	mkdir -p dist/release
	cp dist/$(BINARY)-$(ARCH) dist/release/$(BINARY)
	cp $(BINARY).example.yaml dist/release/$(BINARY).example.yaml
	cp deploy/systemd/$(BINARY).service dist/release/$(BINARY).service
	cp README.md dist/release/README.md 2>/dev/null || true
	cp docs/USERDOC.md dist/release/USERDOC.md 2>/dev/null || true
	tar czf dist/$(BINARY)-$(VERSION)-linux-$(ARCH).tar.gz -C dist/release .
	rm -rf dist/release
	@echo "→ dist/$(BINARY)-$(VERSION)-linux-$(ARCH).tar.gz"

clean:
	rm -rf dist