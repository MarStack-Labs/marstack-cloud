VERSION ?= 0.0.1-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/marstack-labs/marstack-cloud/internal/version.Version=$(VERSION) \
           -X github.com/marstack-labs/marstack-cloud/internal/version.Commit=$(COMMIT)

GOBIN  ?= $(shell go env GOPATH)/bin
PREFIX ?= /usr/local

.PHONY: build install uninstall test vet fmt staticcheck vuln gosec secrets security check tools hooks run stage-images dev-up dev-down dev-logs dev-reset clean

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
	$(GOBIN)/gosec -quiet -severity medium -confidence medium ./...

secrets:
	gitleaks detect --redact --no-banner

security: vuln gosec

check: vet test staticcheck security

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
	rm -rf bin data coverage.out
