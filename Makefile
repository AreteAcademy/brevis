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
NAMESPACE ?= areteacademy
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
	@# -p 1 is NOT a performance choice. Three packages -- scheduler, alerts and
	@# infrastructure/postgres -- each TRUNCATE the shared test database at the
	@# start of every test, and `go test` runs packages in parallel by default.
	@# One package's truncate landing inside another's test made this target a
	@# coin flip: the 100-run acceptance criterion would report "RUNNING = 0,
	@# wanted 5" about a queue somebody else had just emptied.
	@#
	@# CI never hit it because it runs the database tests in two separate
	@# invocations, each touching one truncating package. This target runs them
	@# all at once, which is what a developer types.
	@BREVIS_TEST_DATABASE_URL="$(TEST_DB_URL)" go test -p 1 ./... -count=1

test-db: ## Creates and migrates the test database (idempotent)
	@docker compose exec -T postgres psql -U brevis -d postgres -tAc \
	  "SELECT 1 FROM pg_database WHERE datname='brevis_test'" | grep -q 1 \
	  || docker compose exec -T postgres createdb -U brevis brevis_test
	@BREVIS_DATABASE_URL="$(TEST_DB_URL)" go run ./cmd/brevis migrate up >/dev/null

check: ## gofmt + vet + tests (the gate before committing)
	@test -z "$$(gofmt -l cmd internal migrations)" || { echo "gofmt pending:"; gofmt -l cmd internal migrations; exit 1; }
	@go vet ./...
	@go test ./...
	@# The queue's tests need Postgres and SKIP without it, so this gate is
	@# quieter than CI. A translated assertion in dispatcher_test.go passed here
	@# and failed on master for exactly that reason -- so say what was skipped
	@# rather than let a green `make check` imply more than it checked.
	@test -n "$$BREVIS_TEST_DATABASE_URL" || \
	  echo "note: the queue's tests were skipped (no BREVIS_TEST_DATABASE_URL). CI runs them: make test-int"

dev: ## Hot reload: rebuilds and restarts on every change (needs `make up` first)
	@command -v air >/dev/null || { echo "install it: go install github.com/air-verse/air@latest"; exit 1; }
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
	@command -v templ >/dev/null || { echo "install it: go install github.com/a-h/templ/cmd/templ@$$(go list -m -f '{{.Version}}' github.com/a-h/templ)"; exit 1; }
	@test -x bin/tailwindcss && ./bin/tailwindcss --help 2>&1 | head -1 | grep -q "$(patsubst v%,%,$(TAILWIND_VERSION))" || $(MAKE) tailwind-install
	@templ generate
	@./bin/tailwindcss -i web/assets/app.src.css -o web/assets/app.css --minify

image: generate ## Builds the images for the local architecture (does not publish)
	@docker build --target api  -t $(IMAGE):$(VERSION)        -t $(IMAGE):latest \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) .
	@docker build --target worker -t $(IMAGE):$(VERSION)-worker -t $(IMAGE):latest-worker \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) .
	@echo "  $(IMAGE):$(VERSION)  and  $(IMAGE):$(VERSION)-worker"

image-push: generate ## Publishes multi-arch to the registry (needs `docker login`)
	@docker buildx inspect brevis >/dev/null 2>&1 || docker buildx create --name brevis --use
	@docker buildx build --builder brevis --platform $(PLATAFORMAS) --target api \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .
	@docker buildx build --builder brevis --platform $(PLATAFORMAS) --target worker \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION)-worker -t $(IMAGE):latest-worker --push .
	@echo "published: $(IMAGE):$(VERSION) (+ -worker)"

image-smoke: ## Checks the local images start and report their version
	@docker run --rm $(IMAGE):$(VERSION) version
	@docker run --rm $(IMAGE):$(VERSION)-worker version
	@# --entrypoint: the worker enters through `tini -- brevis`, so a bare `sh`
	@# would become a brevis subcommand. The shell exists for the WORKFLOW.
	@docker run --rm --entrypoint sh $(IMAGE):$(VERSION)-worker -c 'echo "  shell ok in the worker"'

up: ## Brings up Postgres + API + scheduler + the gateway locally
	@docker compose up --build -d
	@echo "api     at http://localhost:$${BREVIS_API_PORT:-8080}/health"
	@echo "gateway at http://localhost:$${BREVIS_GATEWAY_PORT:-8090}/health"
	@echo ""
	@echo "A port already taken -- very often 5432, by another project's"
	@echo "Postgres or one that is merely PAUSED -- fails this with a message"
	@echo "naming a container you have never heard of. Move it instead:"
	@echo "  BREVIS_PG_PORT=55433 make up"

down: ## Tears the local environment down
	@docker compose down

# --- the local cluster -------------------------------------------------------
#
# A Kubernetes cluster with Argo CD on it, and NOTHING deployed. Brevis arrives
# the way a client's would: deployed from Git by Argo CD, not by this Makefile.
# See docker-compose.cluster.yml.
CLUSTER := docker compose -f docker-compose.cluster.yml

.PHONY: cluster-up cluster-down cluster-ui cluster-shell cluster-status cluster-run cluster-watch cluster-tree cluster-dev-image
cluster-up: ## Brings up k3s + Argo CD, with nothing deployed on them
	@$(CLUSTER) up -d --wait k3s
	@$(CLUSTER) run --rm bootstrap
	@$(MAKE) --no-print-directory cluster-ui

cluster-ui: ## The Argo CD URL and the admin password
	@echo "argocd at http://localhost:$${BREVIS_ARGOCD_PORT:-30080}"
	@echo "user      admin"
	@printf 'password  '
	@$(CLUSTER) exec -T k3s kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' 2>/dev/null | base64 -d || echo "(gone: it is deleted once you change the password)"
	@echo

cluster-status: ## What Argo CD has synced, and what is running
	@$(CLUSTER) exec -T k3s kubectl -n argocd get applications
	@echo
	@$(CLUSTER) exec -T k3s kubectl -n dados get pods

cluster-dev-image: ## Builds the agent from THIS tree into the cluster (it is not published yet)
	@docker build -q --target agent -t brevis-agent:dev --build-arg VERSION=dev . >/dev/null
	@docker save brevis-agent:dev | docker exec -i brevis-cluster-k3s-1 ctr -n k8s.io images import - >/dev/null
	@docker build -q -t brevis-tools:dev examples/cluster/runtime >/dev/null
	@docker save brevis-tools:dev | docker exec -i brevis-cluster-k3s-1 ctr -n k8s.io images import - >/dev/null
	@docker build -q -t brevis-goapp:dev examples/cluster/goapp >/dev/null
	@docker save brevis-goapp:dev | docker exec -i brevis-cluster-k3s-1 ctr -n k8s.io images import - >/dev/null
	@echo "brevis-agent:dev, brevis-tools:dev and brevis-goapp:dev are in the cluster's image store"
	@echo "tools is the one the demo runs: the agent, plus python and jq, built by you."
	@echo "The demo pins it with imagePullPolicy: Never, so nothing goes looking for it on a registry."

cluster-goapp: ## The visual test: a Go pod you own, with Brevis running commands in it
	@echo "1. YOUR IMAGE -- a Go binary you wrote, on top of the agent"
	@$(MAKE) --no-print-directory cluster-dev-image >/dev/null
	@echo "   brevis-goapp:dev, built from examples/cluster/goapp"
	@echo
	@echo "2. ARGO CD OWNS IT, because it is declared in Git"
	@$(CLUSTER) exec -T k3s kubectl -n argocd get app brevis-demo \
	  -o jsonpath='{range .status.resources[*]}{.kind}/{.name}{"\n"}{end}' | sed -n 's/^/   /p' | grep goapp
	@echo
	@echo "3. THE POD, started before the run rather than for it"
	@$(CLUSTER) exec -T k3s kubectl -n dados get pods -l app.kubernetes.io/component=goapp \
	  -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,IMAGE:.spec.containers[0].image,STARTED:.status.startTime'
	@before=$$($(CLUSTER) exec -T k3s kubectl -n dados get pods --no-headers 2>/dev/null | wc -l | tr -d ' '); \
	 echo "   pods in the namespace before the run: $$before"; \
	 echo; \
	 echo "4. BREVIS SENDS THE COMMANDS"; \
	 url=$$(curl -fsS -o /dev/null -w '%{redirect_url}' -X POST \
	   http://localhost:$${BREVIS_UI_PORT:-30081}/workflows/goapp_demo/trigger); \
	 echo "   the run, with every line the binary printed: $$url"; \
	 printf "   waiting for the last step"; \
	 for i in $$(seq 1 90); do \
	   if $(CLUSTER) exec -T k3s kubectl -n dados logs brevis-goapp-0 --tail=400 2>/dev/null \
	     | grep -q '"node":"history"'; then break; fi; \
	   printf "."; sleep 2; \
	 done; \
	 echo; echo; \
	 echo "5. WHAT THE POD WAS ASKED TO DO -- one line per command, all on one container"; \
	 $(CLUSTER) exec -T k3s kubectl -n dados logs brevis-goapp-0 --tail=400 \
	   | grep '"msg":"step started"' \
	   | sed -E 's/.*"workflow":"([^"]*)".*"node":"([^"]*)".*/   \1 -> \2/' | tail -4; \
	 echo; \
	 after=$$($(CLUSTER) exec -T k3s kubectl -n dados get pods --no-headers 2>/dev/null | wc -l | tr -d ' '); \
	 echo "6. PODS AFTER THE RUN: $$after, and there were $$before. Brevis created none."; \
	 echo; \
	 echo "   Open $$url for the output of each command,"; \
	 echo "   and http://localhost:$${ARGO_PORT:-30080} for the pod in Argo CD's tree."

cluster-run: ## Triggers a workflow in the cluster: make cluster-run WORKFLOW=pod_per_step
	@curl -fsS -o /dev/null -X POST \
	  http://localhost:$${BREVIS_UI_PORT:-30081}/workflows/$(or $(WORKFLOW),pod_per_step)/trigger
	@echo "queued $(or $(WORKFLOW),pod_per_step) -- watch it with: make cluster-watch"

cluster-watch: ## Watches the step pods appear and die
	@echo "Two pods at once is the point. Ctrl-C to stop."
	@$(CLUSTER) exec -T k3s kubectl -n dados get pods -w \
	  -o 'custom-columns=NAME:.metadata.name,PHASE:.status.phase,IMAGE:.spec.containers[0].image,CPU:.spec.containers[0].resources.requests.cpu,MEM:.spec.containers[0].resources.requests.memory'

cluster-tree: ## What Argo CD OWNS -- the step pods are deliberately not in it
	@$(CLUSTER) exec -T k3s kubectl -n argocd get app brevis-demo \
	  -o jsonpath='{range .status.resources[*]}{.kind}/{.name}{"\n"}{end}'

cluster-shell: ## A kubectl against the local cluster: make cluster-shell ARGS="get pods -A"
	@$(CLUSTER) exec -T k3s kubectl $(ARGS)

cluster-down: ## Tears the cluster down, volumes included
	@$(CLUSTER) down -v

logs: ## Follows the API's logs
	@docker compose logs -f api

smoke: ## Checks /health and /ready against the local environment
	@printf 'health: '; curl -fsS localhost:8080/health && echo
	@printf 'ready:  '; curl -fsS localhost:8080/ready  && echo

.PHONY: help build test test-int test-db check up down logs smoke dev generate tailwind-install image image-push image-smoke
