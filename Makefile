.DEFAULT_GOAL := help
BIN := bin/brevis

# --- Image -----------------------------------------------------------------
# REGISTRY/NAMESPACE are variables so a fork can publish into its own namespace
# without editing any file: `make image-push NAMESPACE=other`.
# Tailwind is PINNED. `releases/latest` made two developers generate different
# CSS from the same source -- and that is what let a stale app.css get past the
# release gate and stamp an image `-dirty`.
TAILWIND_VERSION ?= v4.3.3

REGISTRY  ?= docker.io
NAMESPACE ?= daniel3843
IMAGE    ?= $(REGISTRY)/$(NAMESPACE)/brevis
VERSION    ?= $(shell cat VERSION)
# `-dirty` when there is an uncommitted change. Without the suffix, `brevis
# version` inside the image would point at a commit that does NOT contain the
# published code — and that is the field an incident is traced by.
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo desconhecido)$(shell git diff --quiet HEAD 2>/dev/null || echo -dirty)
BUILD_DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# The two architectures that matter: Apple Silicon in development and
# amd64/arm64 in the cluster. An amd64-only image runs on a Mac through
# emulation, slowly and hiding architecture problems until the deploy.
PLATAFORMAS ?= linux/amd64,linux/arm64
DB_URL := postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable
# A SEPARATE database for the integration tests. Since the local stack started
# bringing up a real scheduler, running the tests against `brevis` was a race:
# the compose's scheduler claimed the items the test had just queued and the
# acceptance criterion failed with nothing being wrong.
TEST_DB_URL := postgres://brevis:brevis@localhost:5432/brevis_test?sslmode=disable

help: ## Lists the targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

# The same -ldflags the image uses. Without them `make build` produced a binary
# reporting `brevis dev`, with no commit and no date, while the documentation
# promised the version stamped in -- and telling a local build from a release
# artifact is exactly what those fields are for.
build: generate ## Builds the binary into bin/ (generates templ and css first)
	@go build -trimpath \
	  -ldflags="-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)" \
	  -o $(BIN) ./cmd/brevis

test: ## Runs the tests (the integration ones skip without Postgres)
	@go test ./...

test-int: test-db ## Runs everything, integration included (needs `make up`)
	@BREVIS_TEST_DATABASE_URL="$(TEST_DB_URL)" go test ./... -count=1

test-db: ## Creates and migrates the test database (idempotent)
	@docker compose exec -T postgres psql -U brevis -d postgres -tAc \
	  "SELECT 1 FROM pg_database WHERE datname='brevis_test'" | grep -q 1 \
	  || docker compose exec -T postgres createdb -U brevis brevis_test
	@BREVIS_DATABASE_URL="$(TEST_DB_URL)" go run ./cmd/brevis migrate up >/dev/null

check: ## gofmt + vet + tests (the gate before committing)
	@test -z "$$(gofmt -l cmd internal migrations)" || { echo "gofmt pendente:"; gofmt -l cmd internal migrations; exit 1; }
	@go vet ./...
	@go test ./...

dev: ## Hot reload: rebuilds and restarts on every change (needs `make up` first)
	@command -v air >/dev/null || { echo "instale: go install github.com/air-verse/air@latest"; exit 1; }
	@test -x bin/tailwindcss || $(MAKE) tailwind-install
	@BREVIS_DATABASE_URL="$(DB_URL)" air

tailwind-install: ## Downloads the standalone Tailwind binary (no Node)
	@mkdir -p bin
	@ARCH=$$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/'); \
	 OS=$$(uname -s | tr 'A-Z' 'a-z' | sed 's/darwin/macos/'); \
	 curl -sSLf -o bin/tailwindcss \
	   "https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$$OS-$$ARCH" \
	 && chmod +x bin/tailwindcss && echo "bin/tailwindcss $(TAILWIND_VERSION) installed"

generate: ## Generates the _templ.go files and the CSS
	@command -v templ >/dev/null || { echo "instale: go install github.com/a-h/templ/cmd/templ@$$(go list -m -f '{{.Version}}' github.com/a-h/templ)"; exit 1; }
	@test -x bin/tailwindcss && ./bin/tailwindcss --help 2>&1 | head -1 | grep -q "$(patsubst v%,%,$(TAILWIND_VERSION))" || $(MAKE) tailwind-install
	@templ generate
	@./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

image: generate ## Builds the images for the local architecture (does not publish)
	@docker build --target api  -t $(IMAGE):$(VERSION)        -t $(IMAGE):latest \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) .
	@docker build --target worker -t $(IMAGE):$(VERSION)-worker -t $(IMAGE):latest-worker \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) .
	@echo "  $(IMAGE):$(VERSION)  e  $(IMAGE):$(VERSION)-worker"

image-push: generate ## Publishes multi-arch to the registry (needs `docker login`)
	@docker buildx inspect brevis >/dev/null 2>&1 || docker buildx create --name brevis --use
	@docker buildx build --builder brevis --platform $(PLATAFORMAS) --target api \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .
	@docker buildx build --builder brevis --platform $(PLATAFORMAS) --target worker \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION)-worker -t $(IMAGE):latest-worker --push .
	@echo "publicado: $(IMAGE):$(VERSION) (+ -worker)"

image-smoke: ## Checks the local images start and report their version
	@docker run --rm $(IMAGE):$(VERSION) version
	@docker run --rm $(IMAGE):$(VERSION)-worker version
	@# --entrypoint: the worker enters through `tini -- brevis`, so a bare `sh`
	@# would become a brevis subcommand. The shell exists for the WORKFLOW.
	@docker run --rm --entrypoint sh $(IMAGE):$(VERSION)-worker -c 'echo "  shell ok in the worker"'

up: ## Brings up Postgres + API + scheduler locally
	@docker compose up --build -d
	@echo "api em http://localhost:8080/health"

down: ## Tears the local environment down
	@docker compose down

logs: ## Follows the API's logs
	@docker compose logs -f api

smoke: ## Checks /health and /ready against the local environment
	@printf 'health: '; curl -fsS localhost:8080/health && echo
	@printf 'ready:  '; curl -fsS localhost:8080/ready  && echo

.PHONY: help build test test-int test-db check up down logs smoke dev generate tailwind-install image image-push image-smoke
