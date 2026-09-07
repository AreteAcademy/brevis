# Shortcuts for the example. `make up` and `make down` are the two that matter.
#
# They exist because driving this by hand is four commands in the right order,
# and getting the order wrong produces a stack that looks up and does nothing:
# the API accepts a run, the queue fills, and the scheduler is not there.
.DEFAULT_GOAL := help

COMPOSE  := docker compose
WORKFLOW ?= daily_sales
# Set it to send failure alerts: make up WEBHOOK=https://hooks.slack.com/...
WEBHOOK  ?=

# NOTE FOR WHOEVER EDITS THIS: no backticks inside a recipe's double quotes.
# `make` hands the line to sh, and a backtick there is command substitution --
# an echo explaining `unless_empty:` tried to RUN unless_empty and printed
# "command not found" in the middle of a working target. Backticks in COMMENTS
# like this one are fine; they never reach a shell.
.PHONY: help
help: ## This list
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: up
up: refill ## Brings the whole stack up and waits until the workflows are published
	@$(COMPOSE) up -d --build $(if $(WEBHOOK),--profile alerting,)
	@printf "waiting for the API"
	@until curl -sf http://localhost:8080/health >/dev/null 2>&1; do printf .; sleep 1; done
	@echo
	@echo "  ready"
	@echo "    UI       http://localhost:8080"
	@echo "    metrics  http://localhost:9090/metrics  (api)"
	@echo "             http://localhost:9091/metrics  (scheduler)"
	@echo
	@echo "  next:  make run   -- queue a run and watch the graph"

.PHONY: down
down: ## Stops everything and forgets the volumes
	@$(COMPOSE) down -v --remove-orphans
	@echo "  down. ./data is bind-mounted, so what the run wrote is still there."

.PHONY: run
run: ## Queues a run of $(WORKFLOW) right now (the UI's button does the same)
	@$(COMPOSE) run --rm --no-deps api backfill $(WORKFLOW) \
	  --from "$$(date -u +%F)" --to "$$(date -u +%F)"
	@echo "  queued. watch it at http://localhost:8080"

.PHONY: logs
logs: ## Follows the scheduler, which is where the steps actually run
	@$(COMPOSE) logs -f scheduler

.PHONY: validate
validate: ## Checks the workflows without a database, the way CI would
	@$(COMPOSE) run --rm --no-deps api validate /workflows

# data/partitions is the source of truth and never moves; data/incoming is what
# the pipeline reads and is rebuilt by these targets. Moving the real files
# around was the first design and it was not idempotent: two `make empty` in a
# row put the generated file in the stash and `make refill` restored four
# partitions.
.PHONY: empty
empty: reset ## One partition with NO rows, so `unless_empty:` skips the report
	@printf 'sku,quantity,price\n' > data/incoming/no-rows.csv
	@echo "  one partition, no rows. The next run LOADS it and then skips"
	@echo "  report, quality and their box on unless_empty: check.has_rows"

.PHONY: nothing
nothing: reset ## No partitions at all, so `for_each:` skips the load itself
	@echo "  no partitions. The next run skips load on an empty list, and"
	@echo "  everything under it says which step stopped it."

.PHONY: refill
refill: reset ## The three real partitions
	@cp data/partitions/*.csv data/incoming/
	@echo "  data/incoming is back to three partitions."

.PHONY: reset
reset:
	@mkdir -p data/incoming
	@rm -f data/incoming/*.csv
	@rm -rf data/landing data/reports

.PHONY: clean
clean: down ## down, plus everything the runs wrote
	@rm -rf data/landing data/reports data/incoming
	@echo "  everything the runs wrote is gone. data/partitions is untouched."
