# Everything runs in Docker: you only need Docker installed (no Go toolchain on the host).
GO_IMAGE ?= golang:1.23
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
UIDGID   := $(shell id -u):$(shell id -g)
RUN_GO    = docker run --rm -v "$(CURDIR)":/src -w /src -e GOFLAGS=-buildvcs=false \
            -v dbanondump-gomod:/go/pkg/mod -v dbanondump-gocache:/root/.cache/go-build $(GO_IMAGE)

.PHONY: build test vet tidy e2e-mysql e2e-mariadb e2e-postgres e2e-all clean

build: ## static binaries for linux/amd64, linux/arm64 and darwin/arm64 in dist/
	@mkdir -p dist
	@for t in linux/amd64 linux/arm64 darwin/arm64; do \
	  os=$${t%/*}; arch=$${t#*/}; echo "build $$os/$$arch"; \
	  $(RUN_GO) env CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/dbanondump-$$os-$$arch ./cmd/dbanondump || exit 1; \
	done
	@docker run --rm -v "$(CURDIR)":/src alpine chown -R $(UIDGID) /src/dist

test: ## unit tests
	$(RUN_GO) go test ./...

vet:
	$(RUN_GO) go vet ./...

tidy: ## resolve the dependencies and write go.sum
	$(RUN_GO) go mod tidy
	@docker run --rm -v "$(CURDIR)":/src alpine chown $(UIDGID) /src/go.mod /src/go.sum

e2e-mysql e2e-mariadb e2e-postgres: build ## small end-to-end test of one engine, incl. restoring the dump
	./test/e2e-engine.sh $(subst e2e-,,$@)

e2e-all: e2e-mysql e2e-mariadb e2e-postgres ## every end-to-end test

clean: ## delete build and output directories
	rm -rf dist output
