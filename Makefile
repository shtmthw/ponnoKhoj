# ponnoKhoj developer entry points. CI (.github/workflows/ci.yml) runs these
# same targets, so "make ci passes locally" means what a green pipeline means
# (minus multi-arch push and signing). `make help` lists the targets.

include deploy/versions.env

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO_TOOL := go tool -modfile=tools/go.mod
COVERAGE_DIR := bin/coverage
FUZZTIME ?= 30s

# Docker Desktop on Linux serves its engine on a per-user socket. Use it when
# DOCKER_HOST is unset and the system socket is absent.
ifeq ($(origin DOCKER_HOST),undefined)
  ifeq ($(wildcard /var/run/docker.sock),)
    ifneq ($(wildcard $(HOME)/.docker/desktop/docker.sock),)
      export DOCKER_HOST := unix://$(HOME)/.docker/desktop/docker.sock
      export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE := /var/run/docker.sock
    endif
  endif
endif

COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DIRTY := $(shell git status --porcelain 2>/dev/null | grep -q . && echo -dirty)
VERSION ?= 0.0.0-dev
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# A dirty tree gets a unique tag so a fresh Job and rollout happen on kind.
# (Simply expanded: the timestamp must not change within one make run.)
ifeq ($(origin IMAGE_TAG),undefined)
  IMAGE_TAG := dev-$(COMMIT)$(if $(DIRTY),-dirty-$(shell date -u +%H%M%S),)
endif
IMAGE ?= ponnokhoj:$(IMAGE_TAG)
LDFLAGS := -s -w \
  -X github.com/shtmthw/ponnoKhoj/internal/version.Version=$(VERSION) \
  -X github.com/shtmthw/ponnoKhoj/internal/version.Commit=$(COMMIT) \
  -X github.com/shtmthw/ponnoKhoj/internal/version.Date=$(DATE)
DOCKER_BUILD_ARGS := --build-arg GO_IMAGE=$(GO_IMAGE) --build-arg RUNTIME_IMAGE=$(RUNTIME_IMAGE) \
  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE)
# Extra buildx flags; CI sets the GitHub Actions layer cache here.
BUILDX_FLAGS ?=
IMAGE_SIZE_BUDGET := 40000000
CRD_SCHEMAS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/$(CRDS_CATALOG_COMMIT)/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
KIND_BASE_URL := http://localhost:8088
# Overlay deployed to kind: dev (real online shops, LLM via .env.dev) or ci
# (offline: demo shop, statistical estimates; what the CI e2e gate runs).
KIND_OVERLAY ?= dev

.PHONY: help
help: ## List the targets
	@grep -hE '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

## ---- tools and code generation -------------------------------------------

.PHONY: tools
tools: ## Build the pinned Go dev tools (tools/go.mod) and devctl
	cd tools && go mod download && go build -o ../bin/devctl ./devctl && go build -o ../bin/covercheck ./covercheck
	$(GO_TOOL) sqlc version
	$(GO_TOOL) golangci-lint version
	$(GO_TOOL) kustomize version
	$(GO_TOOL) kubeconform -v

.PHONY: generate
generate: ## Regenerate sqlc code
	$(GO_TOOL) sqlc generate

.PHONY: generate-check
generate-check: generate ## Fail if generated code is out of date
	@if [ -n "$$(git status --porcelain -- internal/store/db)" ]; then \
	  git status --porcelain -- internal/store/db; git diff -- internal/store/db | head -50; \
	  echo "generated code is out of date or uncommitted: run make generate and commit"; exit 1; fi

## ---- code quality ----------------------------------------------------------

.PHONY: fmt
fmt: ## Format Go code (gofmt + goimports)
	$(GO_TOOL) golangci-lint fmt ./...

.PHONY: lint
lint: ## gofmt, go vet, golangci-lint, go mod tidy/verify
	@out="$$(gofmt -l cmd internal test tools)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	go vet -tags integration ./test/integration/
	go vet -tags e2e ./test/e2e/
	go vet -tags llmlive ./test/llmlive/
	go vet -tags smoke ./test/smoke/
	$(GO_TOOL) golangci-lint run ./...
	cd tools && go tool golangci-lint run ./...
	go mod tidy -diff
	cd tools && go mod tidy -diff
	go mod verify

.PHONY: vuln
vuln: ## govulncheck: fail on reachable vulnerabilities
	$(GO_TOOL) govulncheck ./...

## ---- tests -----------------------------------------------------------------

.PHONY: test
test: ## Unit tests: race detector, shuffled, with coverage
	@mkdir -p $(COVERAGE_DIR)
	go test -race -shuffle=on -count=1 -coverprofile=$(COVERAGE_DIR)/unit.out ./...

.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME (default 30s) on top of its seed corpus
	go test -run='^$$' -fuzz='^FuzzNormalize$$' -fuzztime=$(FUZZTIME) ./internal/search
	go test -run='^$$' -fuzz='^FuzzParsePrice$$' -fuzztime=$(FUZZTIME) ./internal/sources
	go test -run='^$$' -fuzz='^FuzzParseStructuredData$$' -fuzztime=$(FUZZTIME) ./internal/sources
	go test -run='^$$' -fuzz='^FuzzParseProductPage$$' -fuzztime=$(FUZZTIME) ./internal/sources
	go test -run='^$$' -fuzz='^FuzzParseListingCSV$$' -fuzztime=$(FUZZTIME) ./internal/vendor

.PHONY: test-integration
test-integration: ## Integration tests against PostGIS (testcontainers), with coverage
	@mkdir -p $(COVERAGE_DIR)
	go test -tags integration -count=1 -timeout 15m -coverpkg=./internal/... \
	  -coverprofile=$(COVERAGE_DIR)/integration.out ./test/integration/

.PHONY: coverage
coverage: ## Check committed coverage thresholds (needs test + test-integration)
	cd tools && go build -o ../bin/covercheck ./covercheck
	bin/covercheck -thresholds test/coverage-thresholds.json $(COVERAGE_DIR)/unit.out $(COVERAGE_DIR)/integration.out

.PHONY: eval
eval: build ## Baseline estimator backtest against committed thresholds
	bin/ponnokhoj estimate eval --dataset test/fixtures/eval/dataset.json --thresholds test/fixtures/eval/thresholds.json

.PHONY: sources-smoke
sources-smoke: ## Read one live product page per enabled online source (manual; never in CI)
	go test -tags smoke -count=1 -v ./test/smoke/

.PHONY: llm-live
llm-live: ## Run the estimator against the real LLM_BASE_URL/LLM_MODEL (manual; never in CI)
	go test -tags llmlive -count=1 -v -timeout 10m ./test/llmlive/

## ---- build and image -------------------------------------------------------

.PHONY: build
build: ## Build bin/ponnokhoj
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ponnokhoj ./cmd/ponnokhoj

.PHONY: dockerfile-lint
dockerfile-lint: ## hadolint the Dockerfile
	docker run --rm -i $(HADOLINT_IMAGE) hadolint - < Dockerfile

.PHONY: image
image: ## Build the image for this machine's platform as $(IMAGE)
	docker buildx build --load $(BUILDX_FLAGS) $(DOCKER_BUILD_ARGS) -t $(IMAGE) .
	@echo "built $(IMAGE)"

.PHONY: image-multiarch
image-multiarch: ## Build linux/amd64 and linux/arm64 (no push)
	docker buildx build --platform linux/amd64,linux/arm64 $(BUILDX_FLAGS) $(DOCKER_BUILD_ARGS) .

.PHONY: image-check
image-check: image ## Image gates: runs `version`, size budget, Trivy (fixable HIGH/CRITICAL)
	@out="$$(docker run --rm $(IMAGE) version)"; echo "$$out"; \
	  case "$$out" in *"commit $(COMMIT)"*) ;; *) echo "image does not report commit $(COMMIT)"; exit 1;; esac
	@size=$$(docker buildx build -q $(BUILDX_FLAGS) $(DOCKER_BUILD_ARGS) --output type=tar,dest=- . | wc -c); \
	  echo "image filesystem: $$size bytes (budget $(IMAGE_SIZE_BUDGET))"; \
	  [ "$$size" -le $(IMAGE_SIZE_BUDGET) ] || { echo "image exceeds the size budget"; exit 1; }
	@mkdir -p bin
	docker image save $(IMAGE) -o bin/image.tar
	docker run --rm -v "$(CURDIR)/bin:/work:ro" -v ponnokhoj-trivy-cache:/root/.cache $(TRIVY_IMAGE) \
	  image --input /work/image.tar --exit-code 1 --ignore-unfixed --severity HIGH,CRITICAL --no-progress

## ---- Kubernetes ------------------------------------------------------------

.PHONY: manifests
manifests: ## kustomize build each overlay | kubeconform -strict, plus the policy tests
	@mkdir -p bin
	bin/devctl dev-secrets 2>/dev/null || (cd tools && go build -o ../bin/devctl ./devctl && cd .. && bin/devctl dev-secrets)
	for o in dev ci prod; do \
	  echo "== $$o"; \
	  $(GO_TOOL) kustomize build deploy/k8s/overlays/$$o > bin/rendered-$$o.yaml; \
	  $(GO_TOOL) kubeconform -strict -summary -schema-location default -schema-location '$(CRD_SCHEMAS)' bin/rendered-$$o.yaml; \
	done
	$(GO_TOOL) kubeconform -strict -summary -schema-location default -schema-location '$(CRD_SCHEMAS)' deploy/kind/gatewayclass.yaml
	go test -count=1 ./test/policy/

.PHONY: kind-up
kind-up: ## Create the kind cluster with CNPG, cert-manager, Envoy Gateway; build, load and deploy
	cd tools && go build -o ../bin/devctl ./devctl
	bin/devctl kind-up
	$(MAKE) image IMAGE=$(IMAGE)
	bin/devctl kind-load -image $(IMAGE)
	bin/devctl deploy -overlay $(KIND_OVERLAY) -image $(IMAGE)
	@echo
	@echo "ponnoKhoj is running: $(KIND_BASE_URL)  (https://localhost:8443, self-signed)"
	@echo "demo logins: admin@ponnokhoj.test / vendor@ponnokhoj.test, passwords in deploy/k8s/overlays/dev/.env.dev"

.PHONY: kind-down
kind-down: ## Delete the kind cluster
	cd tools && go build -o ../bin/devctl ./devctl
	bin/devctl kind-down

.PHONY: deploy-dev
deploy-dev: ## Build, load and deploy the current tree to the existing kind cluster
	cd tools && go build -o ../bin/devctl ./devctl
	$(MAKE) image IMAGE=$(IMAGE)
	bin/devctl kind-load -image $(IMAGE)
	bin/devctl deploy -overlay $(KIND_OVERLAY) -image $(IMAGE)

.PHONY: deploy-prod
deploy-prod: ## Deploy IMAGE (by digest) to CONTEXT with the prod overlay (owner only)
	@[ -n "$${CONTEXT:-}" ] || { echo "set CONTEXT=<kubectl context of the production cluster>"; exit 1; }
	@case "$(IMAGE)" in *@sha256:*) ;; *) echo "set IMAGE=docker.io/<user>/ponnokhoj@sha256:<digest>"; exit 1;; esac
	cd tools && go build -o ../bin/devctl ./devctl
	bin/devctl deploy -overlay prod -image $(IMAGE) -context $(CONTEXT)

.PHONY: e2e
e2e: ## End-to-end tests against the kind deployment ($(KIND_BASE_URL))
	E2E_BASE_URL=$(KIND_BASE_URL) E2E_KUBE_CONTEXT=kind-ponnokhoj go test -tags e2e -count=1 -timeout 20m -v ./test/e2e/

.PHONY: load
load: ## Load test the kind deployment and record p95 / errors
	go run ./test/load -base-url $(KIND_BASE_URL)

## ---- local stack without Kubernetes ----------------------------------------

.PHONY: dev-up
dev-up: image ## Run Postgres + migrate + seed + web + worker with Docker Compose (http://localhost:8080)
	cd tools && go build -o ../bin/devctl ./devctl
	bin/devctl compose-env
	IMAGE=$(IMAGE) POSTGIS_IMAGE=$(POSTGIS_IMAGE) docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env up -d --wait
	@echo "ponnoKhoj is running: http://localhost:8080 (demo passwords in deploy/compose/.env)"

.PHONY: dev-down
dev-down: ## Stop the Compose stack (keeps the database volume; add -v to drop it)
	IMAGE=$(IMAGE) POSTGIS_IMAGE=$(POSTGIS_IMAGE) docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env down

## ---- everything ------------------------------------------------------------

.PHONY: ci
ci: ## Every CI gate, locally (except multi-arch push and signing)
	$(MAKE) lint
	$(MAKE) generate-check
	$(MAKE) test
	$(MAKE) fuzz
	$(MAKE) test-integration
	$(MAKE) coverage
	$(MAKE) vuln
	$(MAKE) manifests
	$(MAKE) dockerfile-lint
	$(MAKE) image-check
	$(MAKE) eval
	$(MAKE) kind-up KIND_OVERLAY=ci
	$(MAKE) e2e
