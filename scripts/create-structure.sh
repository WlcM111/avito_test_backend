#!/usr/bin/env bash
# Creates the complete directory and file layout of the Avito.Kitchen system
# as fixed by docs/TZ-v3.0.md.
#
# Safe to run repeatedly: it never deletes, truncates or overwrites anything.
# An existing file is left exactly as it is.
#
# Two placeholders are not literally empty, because an empty file would break
# `go build ./...` for the services that are already implemented:
#   * a *.go file gets a single package clause (an empty *.go file is not
#     valid Go);
#   * a cmd/<service>/main.go of a service that is not implemented yet gets a
#     main function that exits with EX_CONFIG and says so, because a `package
#     main` without `func main` cannot be linked.
# Every other placeholder is created as a genuinely empty file.
#
# Usage:  ./scripts/create-structure.sh [target-directory]     (default: .)
set -euo pipefail

ROOT="${1:-.}"
mkdir -p "$ROOT"
cd "$ROOT"

created=0
skipped=0

mkdirs() {
  for d in "$@"; do mkdir -p "$d"; done
}

# touch_file <path> [go-package-name]
touch_file() {
  local path="$1" pkg="${2:-}"
  if [ -e "$path" ]; then
    skipped=$((skipped + 1))
    return 0
  fi
  mkdir -p "$(dirname "$path")"
  if [ -n "$pkg" ]; then
    printf 'package %s\n' "$pkg" > "$path"
  else
    : > "$path"
  fi
  created=$((created + 1))
}

# touch_main <path> <service-name> - placeholder entry point of a service that
# is not implemented yet. It compiles, links and refuses to run.
touch_main() {
  local path="$1" name="$2"
  if [ -e "$path" ]; then
    skipped=$((skipped + 1))
    return 0
  fi
  mkdir -p "$(dirname "$path")"
  cat > "$path" <<EOF
package main

import (
	"fmt"
	"os"
)

// $name is not implemented yet. See docs/TZ-v3.0.md for its contract and the
// iteration in which it is built.
func main() {
	fmt.Fprintln(os.Stderr, "$name is not implemented yet; see docs/TZ-v3.0.md")
	os.Exit(78)
}
EOF
  created=$((created + 1))
}

# ---------------------------------------------------------------- directories
mkdirs \
  .github/workflows \
  api/openapi \
  api/proto/merchant/v1 api/proto/catalog/v1 api/proto/inventory/v1 \
  gen/grpc/merchant/v1 gen/grpc/catalog/v1 gen/grpc/inventory/v1 \
  cmd/merchant-service cmd/catalog-service cmd/inventory-service \
  cmd/order-service cmd/partner-pos cmd/migrate \
  internal/merchant/domain internal/merchant/app internal/merchant/config \
  internal/merchant/testsupport \
  internal/merchant/adapters/httpapi internal/merchant/adapters/grpcapi \
  internal/merchant/adapters/postgres \
  internal/catalog/domain internal/catalog/app internal/catalog/config \
  internal/catalog/adapters/httpapi internal/catalog/adapters/grpcapi \
  internal/catalog/adapters/grpcclient internal/catalog/adapters/postgres \
  internal/inventory/domain internal/inventory/app internal/inventory/config \
  internal/inventory/adapters/httpapi internal/inventory/adapters/grpcapi \
  internal/inventory/adapters/grpcclient internal/inventory/adapters/postgres \
  internal/inventory/jobs internal/inventory/testsupport \
  internal/catalog/testsupport \
  internal/order/domain internal/order/app internal/order/config \
  internal/order/adapters/httpapi internal/order/adapters/grpcclient \
  internal/order/adapters/postgres internal/order/jobs internal/order/testsupport \
  internal/partnerpos/domain internal/partnerpos/app internal/partnerpos/config \
  internal/partnerpos/adapters/httpapi internal/partnerpos/adapters/platform \
  internal/partnerpos/testsupport \
  pkg/logx pkg/envcfg pkg/httpx pkg/pgxx pkg/pgmigrate pkg/grpcx \
  migrations/merchant migrations/catalog migrations/inventory migrations/orders \
  deploy/postgres/init \
  docs/cjm docs/c4 \
  scripts \
  test/testenv test/e2e test/load/k6

# ---------------------------------------------------------------- repo root
touch_file README.md
touch_file Makefile
touch_file go.mod
touch_file .gitignore
touch_file .dockerignore
touch_file .golangci.yml
touch_file .env.example
touch_file docker-compose.yml
touch_file .github/workflows/ci.yml

# ---------------------------------------------------------------- contracts
touch_file api/openapi/merchant.v1.yaml
touch_file api/openapi/catalog.v1.yaml
touch_file api/openapi/inventory.v1.yaml
touch_file api/openapi/order.v1.yaml
touch_file api/openapi/partner-pos.v1.yaml
touch_file api/proto/merchant/v1/merchant.proto
touch_file api/proto/catalog/v1/catalog.proto
touch_file api/proto/inventory/v1/inventory.proto

# --------------------------------------------------- generated gRPC stubs
touch_file gen/grpc/merchant/v1/merchant.pb.go        merchantv1
touch_file gen/grpc/merchant/v1/merchant_grpc.pb.go   merchantv1
touch_file gen/grpc/catalog/v1/catalog.pb.go          catalogv1
touch_file gen/grpc/catalog/v1/catalog_grpc.pb.go     catalogv1
touch_file gen/grpc/inventory/v1/inventory.pb.go      inventoryv1
touch_file gen/grpc/inventory/v1/inventory_grpc.pb.go inventoryv1

# ---------------------------------------------------------------- entrypoints
touch_main cmd/merchant-service/main.go  merchant-service
touch_main cmd/catalog-service/main.go   catalog-service
touch_main cmd/inventory-service/main.go inventory-service
touch_main cmd/order-service/main.go     order-service
touch_main cmd/partner-pos/main.go       partner-pos
touch_main cmd/migrate/main.go           migrate

# ------------------------------------------------------------ merchant-service
touch_file internal/merchant/domain/merchant.go           domain
touch_file internal/merchant/domain/apikey.go             domain
touch_file internal/merchant/domain/errors.go             domain
touch_file internal/merchant/domain/merchant_test.go      domain_test
touch_file internal/merchant/domain/apikey_test.go        domain_test
touch_file internal/merchant/app/ports.go                 app
touch_file internal/merchant/app/service.go               app
touch_file internal/merchant/app/service_test.go          app_test
touch_file internal/merchant/config/config.go             config
touch_file internal/merchant/testsupport/fakes.go         testsupport
touch_file internal/merchant/adapters/httpapi/router.go   httpapi
touch_file internal/merchant/adapters/httpapi/dto.go      httpapi
touch_file internal/merchant/adapters/httpapi/errors.go   httpapi
touch_file internal/merchant/adapters/httpapi/router_test.go httpapi_test
touch_file internal/merchant/adapters/grpcapi/server.go   grpcapi
touch_file internal/merchant/adapters/grpcapi/server_test.go grpcapi_test
touch_file internal/merchant/adapters/postgres/merchant_repo.go postgres
touch_file internal/merchant/adapters/postgres/apikey_repo.go   postgres
touch_file internal/merchant/adapters/postgres/repo_integration_test.go     postgres_test
touch_file internal/merchant/adapters/postgres/helpers_integration_test.go  postgres_test

# ------------------------------------------------------------- catalog-service
touch_file internal/catalog/domain/menu.go               domain
touch_file internal/catalog/domain/menu_snapshot.go      domain
touch_file internal/catalog/domain/errors.go             domain
touch_file internal/catalog/domain/menu_test.go          domain_test
touch_file internal/catalog/domain/menu_snapshot_test.go domain_test
touch_file internal/catalog/app/ports.go                 app
touch_file internal/catalog/app/menu_queries.go          app
touch_file internal/catalog/app/publish_snapshot.go      app
touch_file internal/catalog/app/quote_items.go           app
touch_file internal/catalog/app/service_test.go          app_test
touch_file internal/catalog/config/config.go             config
touch_file internal/catalog/testsupport/fakes.go         testsupport
touch_file internal/catalog/adapters/httpapi/router.go   httpapi
touch_file internal/catalog/adapters/httpapi/dto.go      httpapi
touch_file internal/catalog/adapters/httpapi/errors.go   httpapi
touch_file internal/catalog/adapters/httpapi/router_test.go httpapi_test
touch_file internal/catalog/adapters/grpcapi/server.go   grpcapi
touch_file internal/catalog/adapters/grpcapi/server_test.go grpcapi_test
touch_file internal/catalog/adapters/grpcclient/merchant_client.go grpcclient
touch_file internal/catalog/adapters/postgres/menu_repo.go        postgres
touch_file internal/catalog/adapters/postgres/repo_integration_test.go postgres_test

# ----------------------------------------------------------- inventory-service
touch_file internal/inventory/domain/stock_item.go         domain
touch_file internal/inventory/domain/reservation.go        domain
touch_file internal/inventory/domain/movement.go           domain
touch_file internal/inventory/domain/errors.go             domain
touch_file internal/inventory/domain/stock_item_test.go    domain_test
touch_file internal/inventory/domain/reservation_test.go   domain_test
touch_file internal/inventory/app/ports.go                 app
touch_file internal/inventory/app/stock_commands.go        app
touch_file internal/inventory/app/reservation_commands.go  app
touch_file internal/inventory/app/availability_queries.go  app
touch_file internal/inventory/app/service_test.go          app_test
touch_file internal/inventory/config/config.go             config
touch_file internal/inventory/adapters/httpapi/router.go   httpapi
touch_file internal/inventory/adapters/httpapi/dto.go      httpapi
touch_file internal/inventory/adapters/httpapi/errors.go   httpapi
touch_file internal/inventory/adapters/httpapi/router_test.go httpapi_test
touch_file internal/inventory/adapters/grpcapi/server.go   grpcapi
touch_file internal/inventory/adapters/grpcapi/server_test.go grpcapi_test
touch_file internal/inventory/adapters/grpcclient/merchant_client.go grpcclient
touch_file internal/inventory/testsupport/fakes.go        testsupport
touch_file internal/inventory/adapters/postgres/stock_repo.go       postgres
touch_file internal/inventory/adapters/postgres/reservation_repo.go postgres
touch_file internal/inventory/adapters/postgres/repo_integration_test.go postgres_test
touch_file internal/inventory/jobs/reservation_sweeper.go  jobs
touch_file internal/inventory/jobs/reservation_sweeper_test.go jobs_test

# --------------------------------------------------------------- order-service
touch_file internal/order/domain/order.go            domain
touch_file internal/order/domain/status.go           domain
touch_file internal/order/domain/money.go            domain
touch_file internal/order/domain/outbox.go           domain
touch_file internal/order/domain/errors.go           domain
touch_file internal/order/domain/order_test.go       domain_test
touch_file internal/order/domain/status_test.go      domain_test
touch_file internal/order/app/ports.go               app
touch_file internal/order/app/preview_order.go       app
touch_file internal/order/app/create_order.go        app
touch_file internal/order/app/order_queries.go       app
touch_file internal/order/app/cancel_order.go        app
touch_file internal/order/app/merchant_commands.go   app
touch_file internal/order/app/dispatch_outbox.go     app
touch_file internal/order/app/service_test.go        app_test
touch_file internal/order/config/config.go           config
touch_file internal/order/testsupport/fakes.go       testsupport
touch_file internal/order/adapters/httpapi/customer_router.go httpapi
touch_file internal/order/adapters/httpapi/merchant_router.go httpapi
touch_file internal/order/adapters/httpapi/dto.go             httpapi
touch_file internal/order/adapters/httpapi/errors.go          httpapi
touch_file internal/order/adapters/httpapi/router_test.go     httpapi_test
touch_file internal/order/adapters/grpcclient/merchant_client.go  grpcclient
touch_file internal/order/adapters/grpcclient/catalog_client.go   grpcclient
touch_file internal/order/adapters/grpcclient/inventory_client.go grpcclient
touch_file internal/order/adapters/grpcclient/client_test.go      grpcclient_test
touch_file internal/order/adapters/postgres/order_repo.go   postgres
touch_file internal/order/adapters/postgres/outbox_repo.go  postgres
touch_file internal/order/adapters/postgres/repo_integration_test.go postgres_test
touch_file internal/order/jobs/outbox_dispatcher.go         jobs
touch_file internal/order/jobs/outbox_dispatcher_test.go    jobs_test

# ----------------------------------------------------------------- partner-pos
touch_file internal/partnerpos/domain/menu.go        domain
touch_file internal/partnerpos/domain/decision.go    domain
touch_file internal/partnerpos/domain/decision_test.go domain_test
touch_file internal/partnerpos/app/ports.go          app
touch_file internal/partnerpos/app/publish_menu.go   app
touch_file internal/partnerpos/app/sync_stock.go     app
touch_file internal/partnerpos/app/poll_orders.go    app
touch_file internal/partnerpos/app/decide_order.go   app
touch_file internal/partnerpos/app/service_test.go   app_test
touch_file internal/partnerpos/config/config.go      config
touch_file internal/partnerpos/testsupport/fakes.go  testsupport
touch_file internal/partnerpos/adapters/httpapi/router.go      httpapi
touch_file internal/partnerpos/adapters/httpapi/router_test.go httpapi_test
touch_file internal/partnerpos/adapters/platform/client.go     platform

# ------------------------------------------------------- shared technical code
touch_file pkg/logx/logx.go              logx
touch_file pkg/envcfg/envcfg.go          envcfg
touch_file pkg/httpx/errors.go           httpx
touch_file pkg/httpx/respond.go          httpx
touch_file pkg/httpx/middleware.go       httpx
touch_file pkg/httpx/server.go           httpx
touch_file pkg/httpx/query.go            httpx
touch_file pkg/httpx/ops.go              httpx
touch_file pkg/pgxx/pool.go              pgxx
touch_file pkg/pgxx/tx.go                pgxx
touch_file pkg/pgxx/errors.go            pgxx
touch_file pkg/pgmigrate/migrate.go      pgmigrate
touch_file pkg/grpcx/server.go           grpcx
touch_file pkg/grpcx/client.go           grpcx

# --------------------------------------------------------------- migrations
touch_file migrations/embed.go                    migrations
touch_file migrations/merchant/0001_init.sql
touch_file migrations/merchant/0002_seed.sql
touch_file migrations/catalog/0001_init.sql
touch_file migrations/catalog/0002_seed.sql
touch_file migrations/inventory/0001_init.sql
touch_file migrations/inventory/0002_seed.sql
touch_file migrations/orders/0001_init.sql

# ------------------------------------------------------------------- deploy
touch_file deploy/merchant-service.Dockerfile
touch_file deploy/catalog-service.Dockerfile
touch_file deploy/inventory-service.Dockerfile
touch_file deploy/order-service.Dockerfile
touch_file deploy/partner-pos.Dockerfile
touch_file deploy/migrate.Dockerfile
touch_file deploy/postgres/init/01-databases.sql
touch_file deploy/postgres/init/02-role-passwords.sh

# --------------------------------------------------------------------- docs
touch_file docs/TZ-v3.0.md
touch_file docs/PROJECT-STATE.md
touch_file docs/REQ-MAP-inventory.md
touch_file docs/REQ-MAP-catalog.md
touch_file docs/REQ-MAP-order.md
touch_file docs/REQ-MAP-partnerpos.md
touch_file docs/PROMPT.md
touch_file docs/cjm/customer.puml
touch_file docs/cjm/merchant.puml
touch_file docs/c4/containers.puml

# ------------------------------------------------------------------ scripts
touch_file scripts/create-structure.sh
touch_file scripts/smoke.sh
touch_file scripts/smoke-inventory.sh
touch_file scripts/smoke-catalog.sh
touch_file scripts/smoke-order.sh
touch_file scripts/smoke-pos.sh

# -------------------------------------------------------------------- tests
touch_file test/testenv/postgres.go        testenv
touch_file test/e2e/journey_test.go        e2e_test
touch_file test/load/k6/mixed.js
touch_file test/load/README.md

chmod +x scripts/create-structure.sh scripts/smoke.sh scripts/smoke-inventory.sh \
        scripts/smoke-catalog.sh scripts/smoke-order.sh scripts/smoke-pos.sh \
        deploy/postgres/init/02-role-passwords.sh 2>/dev/null || true

echo "structure ready in $(pwd)"
echo "  created: $created"
echo "  kept:    $skipped"
