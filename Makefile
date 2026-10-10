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
# WHERE THE LOCAL POSTGRES IS PUBLISHED, and every URL below derives from it.
#
# The compose already lets this move -- a developer machine very often has
# something on 5432 -- and these two lines did not follow, which made the
# escape hatch WORSE THAN THE CONFLICT it avoids. `BREVIS_PG_PORT=55433 make
# up` moved the stack and left `make test-int` and `make dev` pointing at
# whatever else answers on 5432: another project's database, migrated by
# `migrate up` if its credentials happened to match.
#
# A port conflict fails loudly. Pointing at a stranger's database does not.
BREVIS_PG_PORT ?= 5432
DB_URL := postgres://brevis:brevis@localhost:$(BREVIS_PG_PORT)/brevis?sslmode=disable
# A SEPARATE database for the integration tests. Since the local stack started
# bringing up a real scheduler, running the tests against `brevis` was a race:
# the compose's scheduler claimed the items the test had just queued and the
# acceptance criterion failed with nothing being wrong.
TEST_DB_URL := postgres://brevis:brevis@localhost:$(BREVIS_PG_PORT)/brevis_test?sslmode=disable

# WHAT `make up-data` NEEDS AND NEVER COMMITS: this machine's own console
# login and session secret. Generated once, 0600, gitignored -- it holds a
# password in the clear, which is the whole reason it is not in the compose.
LOCAL_ENV := .env.local

# THE WAREHOUSE AS THE HOST SEES IT. `brevis-sql serve` runs here and not in
# the compose -- it holds a warehouse credential and nothing in that file
# should -- so it reaches the same database the steps reach, by the other
# address. The registry NAMES this variable; it never holds the string.
BREVIS_WAREHOUSE_PORT ?= 55434

# NOT 8085, which `gcloud auth login` sends the browser to and waits on. A
# container holding that port receives Google's callback instead, and the
# login hangs and then fails with `(missing_code)`. Measured the day it locked
# somebody out of their own project.
BREVIS_PUBSUB_PORT ?= 55085
WAREHOUSE_DSN ?= postgres://brevis:brevis@localhost:$(BREVIS_WAREHOUSE_PORT)/warehouse?sslmode=disable
BREVIS_CONNECTIONS ?= ./examples/full-pipeline/brevis.yaml
SERVE_PID := .brevis-sql-serve.pid
SERVE_LOG := .brevis-sql-serve.log

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

# --- The real warehouse ----------------------------------------------------
# BIGQUERY IS LOCAL AND NEVER IN CI, decided 2026-10-08. The repository is
# public, the only project on hand holds a client's real datasets, and CI
# already says in `Say what did not run` that nothing below that line was
# proven against a real warehouse.
#
# These two targets are the other half of that decision. The script refuses a
# SKIP, which is the opposite of CI and the whole point: a run that exists to
# prove everything ran cannot pass with a test that did not.
warehouse-up: ## Brings up every container the warehouse run needs (floci included)
	@# FIVE PROFILES, not `all`. `queue` and `docstore` have no driver yet --
	@# their own comments in the compose say so -- and starting them would be
	@# four containers nobody looks at. `gcp-native` IS here: two gateway
	@# tests want PUBSUB_EMULATOR_HOST, and leaving it out made them skip,
	@# which the warehouse run refuses.
	@docker compose -f docker-compose.drivers.yml --profile sql --profile aws \
	  --profile gcp --profile metastore up -d
	@# Pub/Sub LAST and tolerated, because `make up`'s stack publishes an
	@# emulator on the same port and whichever came first owns it. The tests
	@# want PUBSUB_EMULATOR_HOST to answer; they do not care which compose
	@# project is answering. This is the cost the drivers compose's own
	@# header warns about -- two project names, one port -- showing up.
	@if nc -z 127.0.0.1 $(BREVIS_PUBSUB_PORT) 2>/dev/null; then \
	  echo "pubsub: $(BREVIS_PUBSUB_PORT) is already served (make up's stack); leaving it"; \
	else \
	  docker compose -f docker-compose.drivers.yml --profile gcp-native up -d; \
	fi
	@for s in postgres mysql; do \
	  for i in $$(seq 1 60); do \
	    [ "$$(docker compose -f docker-compose.drivers.yml ps $$s --format '{{.Health}}')" = healthy ] && break; \
	    sleep 2; \
	  done; \
	done
	@echo "up. Now: export BREVIS_IT_PROJECT / BREVIS_IT_DATASET / BREVIS_IT_BUCKET"

warehouse: ## Every test against the REAL BigQuery, and a skip is a failure (local only)
	@./scripts/warehouse-check.sh

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
	@echo "  export BREVIS_PG_PORT=55433   # then make up"
	@echo ""
	@echo "EXPORT it rather than prefixing one command: every other target"
	@echo "derives its database URL from it, and a stack on one port with"
	@echo "\`make test-int\` on another points at whatever else answers."

# THE WHOLE PRODUCT ON ONE MACHINE, with something in it.
#
# `make up` is the PLATFORM and comes up empty, which is correct for a product
# whose workflows belong to whoever installs it -- and useless for watching it
# work. This adds the three things that were missing: a login, the SQL service
# behind the Preview and Query tabs, and a real project published into it.
#
# The project is `examples/full-pipeline` unless you say otherwise:
#
#     make up-data BREVIS_WORKFLOWS=./my/workflows \
#                  BREVIS_PIPELINE=./my/pipeline \
#                  BREVIS_DATA=./my/data
up-data: $(LOCAL_ENV) warehouse-local serve-up ## The whole stack, with a login, the data tools and a project in it
	@$(MAKE) --no-print-directory demo-data
	@set -a; . ./$(LOCAL_ENV); set +a; docker compose up --build -d
	@echo "compiling the project's steps..."
	@docker compose run --rm --build demo-build >/dev/null
	@echo "publishing its workflows..."
	@docker compose run --rm demo-publish
	@$(MAKE) --no-print-directory gateway-publish
	@printf "waiting for the API"
	@until curl -sf http://localhost:$${BREVIS_API_PORT:-8080}/health >/dev/null 2>&1; \
	  do printf .; sleep 1; done; echo
	@set -a; . ./$(LOCAL_ENV); set +a; \
	  echo ""; \
	  echo "console  http://localhost:$${BREVIS_API_PORT:-8080}/"; \
	  echo "login    $$BREVIS_AUTH_USER / $$BREVIS_LOCAL_PASSWORD"; \
	  echo "gateway  http://localhost:$${BREVIS_GATEWAY_PORT:-8090}/health"; \
	  echo "serve    127.0.0.1:8088   (its log: $(SERVE_LOG))"; \
	  echo ""; \
	  echo "next:  make up-run      -- queue a run and watch the graph"; \
	  echo "       then /data for what it wrote, and its Preview and Query tabs"

# THE WAREHOUSE, UP AND MIGRATED. Its own database and its own container:
# the engine's Postgres is the control plane, and customer data landing in it
# would blur the line this product sells.
#
# The DDL is applied HERE and not only by Postgres's init hook, which runs
# once on an empty volume: editing the schema with a warehouse already created
# would otherwise do nothing, and the first sign would be a failed load.
warehouse-local:
	@docker compose --profile demo up -d warehouse >/dev/null
	@printf "waiting for the warehouse"
	@until docker compose exec -T warehouse pg_isready -U brevis -d warehouse >/dev/null 2>&1; \
	  do printf .; sleep 1; done; echo
	@docker compose exec -T warehouse psql -q -U brevis -d warehouse \
	  < examples/full-pipeline/warehouse/001-sales.sql
	@# NO BACKTICKS inside a recipe's double quotes: make hands the line to
	@# sh and a backtick there is command substitution. The example's own
	@# Makefile carries this warning and this walked into it anyway.
	@echo "warehouse ready on localhost:$${BREVIS_WAREHOUSE_PORT:-55434}, database 'warehouse'"

# WHAT THE GATEWAY WRITES, on /data beside what the workflows write.
#
# A DELIBERATE ACT AND NEVER ITS TRAFFIC. The gateway describes its
# CONFIGURATION -- which streams, which destinations -- and that manifest is
# published; nothing here watches events go by. A destination appears because
# somebody declared it, which is the same rule `publish` follows for a
# workflow.
#
# Through /dev/stdin rather than a shared file: the two containers have no
# directory in common, and inventing one to pass a manifest between them
# would be a mount that outlives the reason for it.
gateway-publish:
	@docker compose run --rm --no-deps -T --entrypoint brevis-gateway \
	  gateway describe /etc/brevis/gateway.yaml 2>/dev/null \
	  | docker compose run --rm --no-deps -T api gateway publish /dev/stdin

# The demo project's partitions, rebuilt. `data/partitions` is the source of
# truth and never moves; `data/incoming` is what the pipeline reads.
#
# Skipped when BREVIS_DATA points somewhere else: that is somebody's own
# project, and a Makefile that copies files into it is a Makefile nobody
# trusts twice.
demo-data:
	@if [ -z "$$BREVIS_DATA" ]; then \
	  mkdir -p examples/full-pipeline/data/incoming; \
	  cp examples/full-pipeline/data/partitions/*.csv examples/full-pipeline/data/incoming/ 2>/dev/null || true; \
	fi

up-run: ## Queues a run of the local project (the console's button does the same)
	@docker compose run --rm --no-deps api backfill $${WORKFLOW:-daily_sales} \
	  --from "$$(date -u +%F)" --to "$$(date -u +%F)"
	@echo "  queued. watch it at http://localhost:$${BREVIS_API_PORT:-8080}"

# THE CREDENTIAL IS MADE, NOT ASKED FOR. `brevis hash` reads a password from
# standard input when there is no terminal -- its own comment calls that case
# a provisioning script -- so this needs nobody to type anything.
#
# Single quotes around every value: a pbkdf2 hash is full of `$`, and `.` on
# an unquoted assignment would expand them into nothing. That is the same
# class of bug as compose's `$$`, one layer down.
$(LOCAL_ENV):
	@pw=$$(openssl rand -base64 18); \
	 hash=$$(printf '%s\n' "$$pw" | go run ./cmd/brevis hash 2>/dev/null); \
	 test -n "$$hash" || { echo "could not generate a password hash"; exit 1; }; \
	 { \
	   echo "# Made by \`make up-data\`. Never committed: it holds a password."; \
	   echo "BREVIS_AUTH_USER='operador'"; \
	   echo "BREVIS_AUTH_PASSWORD_HASH='$$hash'"; \
	   echo "BREVIS_AUTH_SECRET='$$(openssl rand -base64 48 | tr -d '\n')'"; \
	   echo "BREVIS_LOCAL_PASSWORD='$$pw'"; \
	   echo "BREVIS_SQL_SERVE_URL='http://host.docker.internal:8088'"; \
	 } > $(LOCAL_ENV)
	@chmod 600 $(LOCAL_ENV)
	@echo "made $(LOCAL_ENV) -- this machine's console login"

# `brevis-sql serve` runs on the HOST and not in the stack, because it holds a
# warehouse credential and nothing in docker-compose.yml should. Built first
# rather than `go run`: `go run` is a parent whose child survives the kill, so
# `make down` would leave a warehouse reader listening.
#
# AND THE BUILD IS ITS OWN LINE, which is the whole reason this comment is
# longer than it was. `(cd sql && go build …) && VAR=… nohup ./bin/brevis-sql
# … &` backgrounds the WHOLE compound, so `$$!` was the subshell's PID and not
# the reader's -- measured on a running machine: the pidfile said 14688 while
# the listener was 22168. `make down` then killed a PID that had already
# exited, removed the pidfile, and left exactly the warehouse reader the
# paragraph above says it exists to stop. The kill was silent about it
# because `kill … 2>/dev/null && echo` says nothing when the kill fails.
serve-up:
	@if lsof -nP -iTCP:8088 -sTCP:LISTEN >/dev/null 2>&1; then \
	  echo "serve: something already answers on 8088, left alone"; \
	else \
	  (cd sql && go build -o ../bin/brevis-sql ./cmd/brevis-sql); \
	  WAREHOUSE_DSN="$(WAREHOUSE_DSN)" \
	  nohup ./bin/brevis-sql serve --addr 127.0.0.1:8088 \
	    --connections $(BREVIS_CONNECTIONS) > $(SERVE_LOG) 2>&1 & \
	  echo $$! > $(SERVE_PID); \
	  echo "serve: started on 127.0.0.1:8088"; \
	fi

# STOPS WHATEVER HOLDS THE PORT, and not whatever a file remembers.
#
# The pidfile is a hint; the port is the fact. A reader that outlives the
# command meant to stop it is the failure worth preventing here, so this asks
# the machine rather than a file written by an earlier shell.
serve-down:
	@pids="$$(lsof -nP -tiTCP:8088 -sTCP:LISTEN 2>/dev/null)"; \
	if [ -n "$$pids" ]; then \
	  kill $$pids 2>/dev/null; sleep 1; \
	  if lsof -nP -iTCP:8088 -sTCP:LISTEN >/dev/null 2>&1; then \
	    echo "serve: 8088 is STILL served after the kill"; exit 1; \
	  fi; \
	  echo "serve: stopped"; \
	else \
	  echo "serve: nothing on 8088"; \
	fi; \
	rm -f $(SERVE_PID)

# Rebuild and reload the reader without touching the stack, which is what
# changing anything under sql/ needs.
serve-restart: serve-down serve-up ## Rebuilds `brevis-sql serve` and reloads it

down: ## Tears the local environment down
	@docker compose down
	@$(MAKE) --no-print-directory serve-down

# --- the local cluster -------------------------------------------------------
#
# A Kubernetes cluster with Argo CD on it, and NOTHING deployed. Brevis arrives
# the way a client's would: deployed from Git by Argo CD, not by this Makefile.
# See docker-compose.cluster.yml.
CLUSTER := docker compose -f docker-compose.cluster.yml

# THE REVISION ARGO CD DEPLOYS, and it is this branch rather than master.
#
# It defaulted to master, and the failure that caused is the one worth
# remembering: re-running the bootstrap from a feature branch re-pointed both
# Applications at master, both went Synced/Healthy, and the thing being
# demonstrated was simply not in the tree. Green, correct, and about the wrong
# commit.
#
# Argo CD reads GITHUB, not this directory, so the branch has to be pushed --
# which cluster-up refuses to proceed without rather than leaving Argo to say
# it in a ComparisonError nobody reads.
export BREVIS_GIT_REVISION ?= $(shell git rev-parse --abbrev-ref HEAD)

.PHONY: cluster-up cluster-down cluster-ui cluster-shell cluster-status cluster-run cluster-watch cluster-tree cluster-dev-image cluster-goapp cluster-revision
cluster-up: ## Brings up k3s + Argo CD, with nothing deployed on them
	@$(MAKE) --no-print-directory cluster-revision
	@$(CLUSTER) up -d --wait k3s
	@$(CLUSTER) run --rm bootstrap
	@$(MAKE) --no-print-directory cluster-ui

cluster-revision: ## Refuses a revision GitHub does not have -- Argo CD deploys from there, not from here
	@git ls-remote --exit-code --heads --tags origin "$(BREVIS_GIT_REVISION)" >/dev/null 2>&1 || { \
	  echo "origin has no $(BREVIS_GIT_REVISION), and that is where Argo CD reads from."; \
	  echo "push it first:  git push origin $(BREVIS_GIT_REVISION)"; \
	  exit 1; }
	@here=$$(git rev-parse --short HEAD); there=$$(git rev-parse --short "origin/$(BREVIS_GIT_REVISION)" 2>/dev/null || echo ""); \
	 if [ -n "$$there" ] && [ "$$here" != "$$there" ]; then \
	   echo "note: this tree is at $$here and origin/$(BREVIS_GIT_REVISION) at $$there."; \
	   echo "      Argo CD deploys origin's. Push if you meant this one."; \
	 fi
	@echo "Argo CD will deploy $(BREVIS_GIT_REVISION)"

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

# It waits on THE RUN and not on the agent's log. Waiting on the log matched
# the previous run's line, broke out instantly, and printed four steps of which
# two belonged to the run before -- a test that reported a result it had not
# waited for, which is the only kind of test worth less than none.
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
	 printf "   waiting for it to finish"; \
	 state=""; \
	 for i in $$(seq 1 90); do \
	   state=$$(curl -fsS "http://localhost:$${BREVIS_UI_PORT:-30081}/api/runs/$${url##*/}/graph" \
	     | sed -E 's/.*"status":"([a-z]+)".*"terminal":(true|false).*/\1 \2/'); \
	   case "$$state" in *" true") break;; esac; \
	   printf "."; sleep 2; \
	 done; \
	 echo; echo "   the run ended: $${state%% *}"; echo; \
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

smoke: ## Checks the api's /health and /ready, and that the gateway accepts an event
	@printf 'health: '; curl -fsS localhost:$${BREVIS_API_PORT:-8080}/health && echo
	@printf 'ready:  '; curl -fsS localhost:$${BREVIS_API_PORT:-8080}/ready  && echo
	@# A POST and not /health: the gateway's /health answers before its config
	@# is proven, and a gateway restarting on a hook it does not have (#68)
	@# answers nothing at all. 202 is the one answer that means it took the event.
	@printf 'gateway: '; curl -sS -o /dev/null -w '%{http_code}' -X POST \
	  localhost:$${BREVIS_GATEWAY_PORT:-8090}/v1/clicks -H 'Content-Type: application/json' \
	  -d '{"event_id":"smoke","occurred_at":"2026-01-01T00:00:00Z","host":"smoke.example.com","region":"local"}' \
	  | grep -q '^202$$' && echo 202 || { echo "not 202: see 'docker compose logs gateway'"; exit 1; }

.PHONY: help build test test-int test-db check up down serve-up serve-down serve-restart logs smoke dev generate tailwind-install image image-push image-smoke
