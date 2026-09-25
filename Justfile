# Smart ERP developer commands. Every agent uses these; CI calls the same targets.
# Requires: docker (Desktop on Mac, Engine on NUC), go 1.27+, just, sqlc, golangci-lint, goose, syft, go-licenses, oapi-codegen.

set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := false

compose_dir := "deploy/compose"
api_dir     := "apps/api"
host_env    := "deploy/compose/.env.dev.host"
profile     := env_var_or_default("ERP_PROFILE", "dev")

export PATH := env_var("HOME") + "/go/bin:" + env_var("PATH")

default:
    @just --list

# --- stack -------------------------------------------------------------------

# Bring up the local stack (infra services only; api/worker run from source with `just run-*`).
up: _envfiles
    cd {{compose_dir}} && docker compose --profile {{profile}} up -d --remove-orphans postgres valkey minio minio-init zitadel caddy headless-shell mailpit otel-collector
    @just _wait-postgres
    @echo "stack is up: postgres :5432, valkey :6379, minio :9000/:9001, zitadel :8081, mailpit :8025, caddy :8443"
    @echo "run the Go services from source with: just run-api | just run-worker | just run-scheduler (or 'just up-all' for containers)"

# Bring up everything including the containerised api/worker/scheduler (builds images).
up-all: _envfiles
    cd {{compose_dir}} && docker compose --profile {{profile}} up -d --build --remove-orphans

down:
    cd {{compose_dir}} && docker compose --profile {{profile}} down --remove-orphans

# Destroy volumes too. Asks for confirmation.
nuke:
    @read -p "This deletes all local data volumes. Type yes: " a && [ "$a" = "yes" ]
    cd {{compose_dir}} && docker compose --profile {{profile}} down -v --remove-orphans

ps:
    cd {{compose_dir}} && docker compose --profile {{profile}} ps

logs service="":
    cd {{compose_dir}} && docker compose --profile {{profile}} logs -f --tail=200 {{service}}

_envfiles:
    @[ -f {{compose_dir}}/.env ] || cp {{compose_dir}}/.env.dev.example {{compose_dir}}/.env
    @[ -f {{host_env}} ] || cp {{compose_dir}}/.env.dev.host.example {{host_env}}

_wait-postgres:
    @for i in $(seq 1 60); do docker compose -f {{compose_dir}}/docker-compose.yml --profile {{profile}} exec -T postgres pg_isready -U postgres -q 2>/dev/null && exit 0; sleep 1; done; echo "postgres not ready" >&2; exit 1

# --- database ----------------------------------------------------------------

# Apply migrations (migrator, superuser, River) and refresh erp_template.
migrate: _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go run ./cmd/migrate up

# Recreate erp_template from erp (after manual schema work).
template: _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go run ./cmd/migrate template

# Drop and recreate the erp database, then migrate. Local only.
reset: _envfiles
    set -a; source {{host_env}}; set +a; \
    docker compose -f {{compose_dir}}/docker-compose.yml --profile {{profile}} exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -c "UPDATE pg_database SET datistemplate=false WHERE datname='erp_template'" -c "DROP DATABASE IF EXISTS erp_template WITH (FORCE)" -c "DROP DATABASE IF EXISTS erp WITH (FORCE)" -c "CREATE DATABASE erp OWNER erp_migrator" && \
    docker compose -f {{compose_dir}}/docker-compose.yml --profile {{profile}} exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -d erp -c "REVOKE ALL ON SCHEMA public FROM PUBLIC" -c "GRANT ALL ON SCHEMA public TO erp_migrator" -c "GRANT USAGE ON SCHEMA public TO erp_app"
    @just migrate

# Create a named per-agent test database from erp_template. Use with ERP_TEST_DATABASE=<name>.
db name: _envfiles
    set -a; source {{host_env}}; set +a; \
    docker compose -f {{compose_dir}}/docker-compose.yml --profile {{profile}} exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS erp_test_{{name}} WITH (FORCE)" -c "CREATE DATABASE erp_test_{{name}} TEMPLATE erp_template OWNER erp_migrator"
    @echo "export ERP_TEST_DATABASE=erp_test_{{name}}"

# Scaffold a goose migration for a new immutable table: just new-immutable erp.widgets 00010_widgets
new-immutable table name:
    cd {{api_dir}} && go run ./cmd/migrate new-immutable {{table}} > migrations/{{name}}.sql && echo "wrote migrations/{{name}}.sql (needs the immutable-change label on the PR)"

# Seed personas, chart of accounts, sample parties and SKUs (Wave 2 fills this in).
seed: _envfiles
    @echo "seed: no seed data yet (Phase 2, task P2.1/P2.2)"

# Install repo git hooks (pre-push refuses direct pushes to main).
hooks:
    git config core.hooksPath .githooks && echo "hooks installed (core.hooksPath=.githooks)"

# All acceptance cases against the composed API and Postgres. Requires the dev compose stack.
e2e: _envfiles
    cd {{compose_dir}} && docker compose --profile {{profile}} up -d --remove-orphans postgres valkey minio minio-init zitadel caddy headless-shell mailpit otel-collector
    @just _wait-postgres
    @just migrate
    @just template
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go test ./e2e -count=1 -timeout 45m

# Run code generation: sqlc, oapi-codegen, canonical fixtures.
gen:
    cd {{api_dir}} && go generate ./...
    cd {{api_dir}} && CANON_WRITE_FIXTURES=1 go test ./internal/kit/canon -run TestFixtures -count=1 >/dev/null && echo "canonical fixtures regenerated"

build:
    cd {{api_dir}} && go build ./...

lint:
    cd {{api_dir}} && golangci-lint run ./...

# Unit tests only (no database).
test-unit:
    cd {{api_dir}} && go test ./... -short -count=1

# All tests against the running stack (creates erp_test_<rand> databases from erp_template).
test pkg="./...": _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go test {{pkg}} -race -count=1

test-all: test

# Licence scan: fails on forbidden/restricted; MPL-2.0 (reciprocal) is allowed unmodified per ADR-14.
licence:
    cd {{api_dir}} && go-licenses check ./... --disallowed_types=forbidden,restricted --ignore github.com/jaichahal/smart-erp

sbom:
    mkdir -p dist && syft dir:{{api_dir}} -o spdx-json > dist/sbom.spdx.json && echo "dist/sbom.spdx.json"

openapi-check:
    @command -v npx >/dev/null && npx -y @redocly/cli lint contracts/openapi/openapi.yaml || docker run --rm -v "$PWD:/spec" redocly/cli lint /spec/contracts/openapi/openapi.yaml

# --- run from source ---------------------------------------------------------

run-api: _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go run ./cmd/api

run-worker: _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go run ./cmd/worker

run-scheduler: _envfiles
    set -a; source {{host_env}}; set +a; cd {{api_dir}} && go run ./cmd/scheduler

# --- operations (Track I fills these in; stubs call deploy/scripts) -----------

backup-now:
    deploy/scripts/backup.sh

anchor-now:
    deploy/scripts/anchor.sh

verify-chain:
    deploy/scripts/verify-chain.sh

drive-check:
    deploy/scripts/drive-check.sh

drill target="offsite" backup="latest":
    deploy/scripts/restore.sh --drill --target {{target}} --backup {{backup}}

restore target backup approval:
    deploy/scripts/restore.sh --target {{target}} --backup {{backup}} --approval {{approval}}

# Print the multi-arch build command CI runs.
images:
    @echo "docker buildx build --platform linux/amd64,linux/arm64 --build-arg CMD=api -t ghcr.io/jaichahal/smart-erp/api:dev apps/api"
