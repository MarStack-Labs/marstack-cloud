VERSION ?= 0.0.1-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/marstack-labs/marstack-cloud/internal/version.Version=$(VERSION) \
           -X github.com/marstack-labs/marstack-cloud/internal/version.Commit=$(COMMIT)

.PHONY: build test vet run clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/marstack ./cmd/marstack

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/marstack server

clean:
	rm -rf bin data
