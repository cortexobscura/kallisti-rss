.PHONY: deps fmt vet test test-race fuzz security build check serve-demo docker release-check check-deploy
GO ?= go
GOVULNCHECK_VERSION ?= v1.9.0
GOSEC_VERSION ?= v2.30.0

deps:
	$(GO) mod download
fmt:
	gofmt -w cmd internal
vet:
	$(GO) vet ./...
test:
	$(GO) test -count=1 ./...
test-race:
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
fuzz:
	$(GO) test ./internal/bot -run '^$$' -fuzz FuzzParse -fuzztime 10s -parallel 2
security:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kallisti-rss ./cmd/kallisti-rss
	cp bin/kallisti-rss bin/kallisti-feedbot
check: vet test-race build
	@test -z "$$(gofmt -l cmd internal)"
serve-demo:
	$(GO) run ./cmd/kallisti-rss -demo
docker:
	docker build -t kallisti-rss:local .

release-check: check serve-demo fuzz security

check-deploy: build
	sh scripts/check-deploy.sh
