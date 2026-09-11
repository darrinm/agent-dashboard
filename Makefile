.PHONY: build test test-ui run collector linux clean
export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/gomod

build:
	bash scripts/build.sh

test:
	go test -race ./...

test-ui:
	bash scripts/test-ui.sh

run: build
	open dist/Agents.app --args --show

collector:
	go build -trimpath -o dist/agents-collector ./cmd/agents-collector

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/agents-collector-linux-amd64 ./cmd/agents-collector
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/agents-collector-linux-arm64 ./cmd/agents-collector

clean:
	rm -rf dist .build
