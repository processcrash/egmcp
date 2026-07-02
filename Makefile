# egmcp Makefile
#
# Most tasks delegate to a Go + Node toolchain. If Go or Node are not
# installed locally, use `make docker-build` to produce the image
# instead — the image bundles the full build pipeline.
#
# Conventions:
#   - `make help` lists targets.
#   - Image name is `egmcp`. Override with `IMAGE=ghcr.io/me/egmcp make build`.

IMAGE ?= egmcp
TAG   ?= dev

# Paths
BIN_DIR := bin
GO      ?= go
NODE    ?= node
NPM     ?= npm
DOCKER  ?= docker

.PHONY: help tidy build web web-build test fmt vet lint run docker-build docker-run docker-stop clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

tidy: ## go mod tidy
	$(GO) mod tidy

build: ## Build backend binary into bin/egmcp
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -ldflags "-s -w" -o $(BIN_DIR)/egmcp ./cmd/egmcp

web-install: ## Install frontend dependencies
	cd web && $(NPM) install

web-build: ## Build frontend assets (output: web/dist)
	cd web && $(NPM) run build

test: ## Run all unit tests
	$(GO) test ./...

fmt: ## gofmt the whole tree
	$(GO) fmt ./...

vet: ## go vet the whole tree
	$(GO) vet ./...

lint: vet ## alias to vet for now (add golangci-lint later)

run: ## Run the backend in the foreground (assumes configs/admin.yaml exists)
	$(GO) run ./cmd/egmcp

docker-build: ## Build the Docker image
	$(DOCKER) build -t $(IMAGE):$(TAG) .

docker-run: ## Run the Docker image in the foreground
	$(DOCKER) run --rm -p 8080:8080 -v egmcp-data:/data --name egmcp-dev $(IMAGE):$(TAG)

docker-stop: ## Stop the dev container
	$(DOCKER) rm -f egmcp-dev 2>/dev/null || true

clean: ## Remove build artefacts
	rm -rf $(BIN_DIR) web/dist
