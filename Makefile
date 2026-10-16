# Avito.Kitchen — developer entry points.
# Dependencies are resolved from the Go module proxy; run `make deps` once on a
# cold cache (it needs network access).

GO             ?= go
SERVICE        ?= merchant
TEST_DATABASE_URL ?= postgres://postgres:postgres@127.0.0.1:5499/postgres?sslmode=disable

# The stack started by `make up`, as its ports are published on the host
# (defaults of docker-compose.yml and .env.example). Override them when the
# ports in .env differ, e.g. `make e2e E2E_ORDER_URL=http://localhost:18084`.
E2E_MERCHANT_URL  ?= http://localhost:8081
E2E_CATALOG_URL   ?= http://localhost:8082
E2E_INVENTORY_URL ?= http://localhost:8083
E2E_ORDER_URL     ?= http://localhost:8084
E2E_POS_URL       ?= http://localhost:8085
# Ops listeners (/readyz) of merchant, inventory, catalog, order and partner-pos.
OPS_PORTS         ?= 9091 9093 9092 9094 9095
# How long `make wait` waits for each service, in seconds.
WAIT_TIMEOUT      ?= 180

.PHONY: help
help: ## Show the available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Download the dependencies listed in go.mod (needs network)
	$(GO) mod download

.PHONY: build
build: ## Compile every command
	$(GO) build ./...

.PHONY: fmt
fmt: ## Format the sources
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*' -not -path './gen/*' -not -path './_*')

.PHONY: fmt-check
fmt-check: ## Fail when the sources are not formatted
	@out="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*' -not -path './gen/*' -not -path './_*'))"; \
	if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run the standard static analysis
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint when it is installed
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; \
	else echo "golangci-lint is not installed; running go vet instead"; $(GO) vet ./...; fi

.PHONY: verify
verify: ## Check that the dependency checksums match go.sum
	$(GO) mod verify

.PHONY: test
test: ## Run the unit tests (no database required)
	$(GO) test ./...

.PHONY: test-integration
test-integration: ## Run every test including the ones that need PostgreSQL
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -count=1 ./...

.PHONY: test-race
test-race: ## Run the full suite under the race detector
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -race -count=1 ./...

.PHONY: check
check: fmt-check vet lint verify test-race ## What the CI jobs build-and-test and lint check

.PHONY: generate
generate: ## Regenerate the gRPC stubs from api/proto
	protoc -I api/proto \
		--go_out=. --go_opt=module=github.com/avito-kitchen/platform \
		--go-grpc_out=. --go-grpc_opt=module=github.com/avito-kitchen/platform \
		api/proto/merchant/v1/merchant.proto \
		api/proto/catalog/v1/catalog.proto \
		api/proto/inventory/v1/inventory.proto

.PHONY: migrate
migrate: ## Apply the migrations of $(SERVICE) to $(DATABASE_URL)
	MIGRATE_SERVICE=$(SERVICE) $(GO) run ./cmd/migrate

.PHONY: up
up: ## Build and start the whole stack in the background
	docker compose up --build -d

.PHONY: wait
wait: ## Wait until every service of the running stack reports ready
	@for port in $(OPS_PORTS); do \
		printf 'waiting for http://localhost:%s/readyz ... ' "$$port"; \
		elapsed=0; \
		until curl -fs -m 5 -o /dev/null "http://localhost:$$port/readyz"; do \
			if [ "$$elapsed" -ge "$(WAIT_TIMEOUT)" ]; then \
				echo "not ready after $(WAIT_TIMEOUT)s, see: docker compose ps; docker compose logs"; \
				exit 1; \
			fi; \
			sleep 2; elapsed=$$((elapsed + 2)); \
		done; \
		echo ready; \
	done

.PHONY: down
down: ## Stop the local environment and drop its data
	docker compose down -v

.PHONY: logs
logs: ## Follow the service logs
	docker compose logs -f merchant-service inventory-service catalog-service order-service partner-pos

.PHONY: smoke
smoke: ## Call the running API and print the results
	./scripts/smoke.sh
	./scripts/smoke-inventory.sh
	./scripts/smoke-catalog.sh
	./scripts/smoke-order.sh
	./scripts/smoke-pos.sh

.PHONY: e2e
e2e: ## Run the end-to-end tests against the running stack
	E2E_MERCHANT_URL="$(E2E_MERCHANT_URL)" E2E_CATALOG_URL="$(E2E_CATALOG_URL)" \
	E2E_INVENTORY_URL="$(E2E_INVENTORY_URL)" E2E_ORDER_URL="$(E2E_ORDER_URL)" \
	E2E_POS_URL="$(E2E_POS_URL)" $(GO) test -count=1 -v ./test/e2e/...

.PHONY: stack-check
stack-check: up wait smoke e2e ## Start the stack, wait for it, run the smoke scripts and the end-to-end tests
