VERSION ?= 0.0.1-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/marstack-labs/marstack-cloud/internal/version.Version=$(VERSION) \
           -X github.com/marstack-labs/marstack-cloud/internal/version.Commit=$(COMMIT)

GOBIN  ?= $(shell go env GOPATH)/bin
PREFIX ?= /usr/local

DIST    ?= dist
TARGETS ?= linux/amd64 linux/arm64 darwin/arm64

.PHONY: build install uninstall test vet cross dist fmt staticcheck vuln gosec secrets security check tools hooks run stage-images dev-up dev-down dev-logs dev-reset clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/marstack ./cmd/marstack

install:
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 bin/marstack $(DESTDIR)$(PREFIX)/bin/marstack

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/marstack

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

staticcheck:
	$(GOBIN)/staticcheck ./...

vuln:
	$(GOBIN)/govulncheck ./...

gosec:
	$(GOBIN)/gosec -quiet -severity medium -confidence medium -exclude=G204,G301,G302,G304,G306,G703 ./...

secrets:
	gitleaks detect --redact --no-banner

security: vuln gosec

check: vet cross test staticcheck security

cross:
	GOOS=linux GOARCH=arm64 go build ./...
	GOOS=linux GOARCH=arm64 go vet ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go build ./...

dist:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@for target in $(TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		stage=$(DIST)/marstack_$(VERSION)_$${os}_$${arch}; \
		mkdir -p $$stage || exit 1; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$stage/marstack ./cmd/marstack || exit 1; \
		cp LICENSE README.md $$stage/ || exit 1; \
		tar -C $(DIST) -czf $$stage.tar.gz $$(basename $$stage) || exit 1; \
		rm -rf $$stage; \
	done
	cd $(DIST) && shasum -a 256 *.tar.gz > SHA256SUMS
	@ls -l $(DIST)

tools:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest

hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit

run: build
	./bin/marstack server

DEV_DATA ?= $(HOME)/marstack-data
DEV_LOGS ?= $(HOME)
DEV_NODE ?= bm-1
DEV_ZONE ?= rack-a

stage-images:
	sudo -E scripts/stage-images.sh

dev-up: build
	@$(MAKE) --no-print-directory dev-down
	setsid nohup ./bin/marstack server --data-dir $(DEV_DATA) > $(DEV_LOGS)/ms-server.log 2>&1 < /dev/null &
	@sleep 2
	setsid sudo nohup ./bin/marstack agent --name $(DEV_NODE) --zone $(DEV_ZONE) > $(DEV_LOGS)/ms-agent.log 2>&1 < /dev/null &
	@sleep 3
	@./bin/marstack node list

dev-down:
	@for p in $$(pgrep -x marstack); do sudo kill $$p 2>/dev/null || true; done; sleep 1

dev-logs:
	@tail -n 30 $(DEV_LOGS)/ms-server.log $(DEV_LOGS)/ms-agent.log

dev-reset: dev-down
	rm -rf $(DEV_DATA)
	sudo rm -rf /var/lib/marstack/instances

clean:
	rm -rf bin dist data coverage.out
