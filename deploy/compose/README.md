# smart-erp compose stack

One compose project, `smart-erp`, drives both targets (spec 10, ADR-16):

| | dev (Apple Silicon Mac) | prod (x86_64 Linux NUC) |
| --- | --- | --- |
| files | `docker-compose.yml` + `compose.dev.yml` | `docker-compose.yml` + `compose.prod.yml` |
| profile | `dev` | `prod` |
| env file | `.env` from `.env.dev.example` | `.env` decrypted from `/etc/smart-erp/env.age` |
| extra services | mailpit, pushsink, `offsite-sim` bucket | none; real off-site bucket, KMS, OTLP sink |
| ingress | Caddy `https://api.localhost:8443`, internal CA | Caddy automatic TLS on `ERP_PUBLIC_HOSTNAME` |

The env file selects the file set and profile (`COMPOSE_FILE`, `COMPOSE_PROFILES`), so plain `docker compose ...` does the right thing once `.env` exists. Overrides may only differ in secrets, hostnames, resource limits, and off-site endpoints.

## Bring it up (dev)

```sh
cd deploy/compose
cp .env.dev.example .env
docker compose config -q                 # validate
docker compose up -d                     # whole stack, incl. api/worker/scheduler builds
docker compose up -d postgres valkey minio minio-init zitadel mailpit headless-shell otel-collector caddy   # infra only
docker compose ps                        # wait for (healthy); zitadel takes 60-90 s the first time
docker compose watch api                 # rebuild api on save (develop.watch)
docker compose down                      # keep data
docker compose down -v                   # wipe data (postgres init SQL runs again on next up)
```

`just up` / `just down` / `just reset` wrap these. For Go binaries and tests running directly on the Mac, source `.env.dev.host.example` (service names replaced with `localhost`).

## What is where

| service | image | host ports (dev) | notes |
| --- | --- | --- | --- |
| postgres | `postgres:16-alpine` | 5432 | `postgres-init/*.sql` creates roles `erp_migrator`, `erp_app`, `zitadel`, databases `erp`, `zitadel`; `log_statement=ddl`, `log_connections=on` |
| valkey | `valkey/valkey:8-alpine` | 6379 | AOF on |
| minio | `cgr.dev/chainguard/minio:latest` | 9000 API, 9001 console | `minioadmin`/`minioadmin` in dev |
| minio-init | `cgr.dev/chainguard/minio-client:latest-dev` | | one-shot; buckets `erp-files`, `erp-backups`, `erp-anchors` (+ `offsite-sim` in dev), all with object lock; compliance default retention `1d` dev / `7y` prod on anchors |
| zitadel | `ghcr.io/zitadel/zitadel:v3.4.15` | 8081 | `admin` / `Admin1234!`; machine user `erp-api`, PAT at `/zitadel-pat/api.pat` (shared volume `zitadel-pat`) |
| caddy | `caddy:2-alpine` | 8443 https, 8880 http | `Caddyfile` dev, `Caddyfile.prod` prod |
| headless-shell | `chromedp/headless-shell:latest` | none | CDP on `headless-shell:9222` inside the network |
| mailpit | `axllent/mailpit:latest` | 8025 UI, 1025 SMTP | dev only |
| otel-collector | `otel/opentelemetry-collector-contrib:latest` | 4317, 4318 | dev writes `/var/log/otel/traces.json` (volume `otel-logs`); prod ships to `ERP_OTLP_UPSTREAM` |
| migrate | built, `CMD=migrate` | | runs once; api/worker/scheduler wait for it |
| api | built, `CMD=api` | 8080 | `develop.watch` on `apps/api` |
| worker, scheduler | built | | |
| pushsink | built, `CMD=pushsink` | 8090 | dev only |
| volume-init | `busybox:1.37` | | one-shot chown of `zitadel-pat` and `otel-logs` volumes |

Why MinIO comes from Chainguard: on 2026-09-25 the upstream `minio/minio` and `minio/mc` images were no longer published on Docker Hub (namespace 404) and quay.io required authentication. `cgr.dev/chainguard/minio` is the unmodified upstream server, rebuilt daily, multi-arch, and bundles `mc`.

Port 8080 belongs to `api`; Caddy's plain-http listener is therefore on 8880 in dev (prod uses 443/80).

## Useful checks

```sh
docker compose exec postgres psql -U postgres -c '\du'          # three roles
docker compose exec postgres psql -U postgres -l               # erp, zitadel
docker compose exec minio bash -c 'mc alias set erp http://127.0.0.1:9000 minioadmin minioadmin && mc retention info --default erp/erp-anchors'
curl -s http://localhost:8081/debug/healthz                      # ok
docker compose cp zitadel:/zitadel-pat/api.pat ~/.smart-erp/api.pat   # PAT for host-side runs
docker run --rm -v smart-erp_otel-logs:/l busybox:1.37 tail -n1 /l/traces.json   # last exported span (otel image has no shell)
```

Trust the dev CA once so `https://api.localhost:8443` works in browsers:

```sh
docker compose exec caddy cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy-root.crt
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/caddy-root.crt
```

## Production

`deploy/nuc/bootstrap.sh` installs Docker, Tailscale, the systemd units (`deploy/systemd/`) and copies this directory to `/opt/smart-erp/deploy/compose`. `smart-erp.service` decrypts `/etc/smart-erp/env.age` to `.env`, validates, pulls, and runs `docker compose --profile prod up -d --wait`. `.env.prod.example` lists every variable; `compose.prod.yml` uses `${VAR:?}` so a missing secret fails fast.

Pin by digest for a release: append `@sha256:...` to each `image:` in `docker-compose.yml` (CI prints the digests it pushed). Rollback is re-pinning the previous digests and `systemctl reload smart-erp.service`.

## Healthchecks and their quirks

- postgres: `pg_isready` over TCP, so the init-phase temporary server (unix socket only) does not count as healthy.
- minio, headless-shell: no curl/wget/grep in the images; pure-bash `/dev/tcp` probes.
- zitadel: scratch image; `zitadel ready` probes its own `/debug/ready`. `/debug/healthz` is checked from the host.
- otel-collector: bare binary, no probe possible from inside; `health_check` extension on `:13133` for external checks.
- api/worker/scheduler: distroless, no shell; add a Go-side `/healthz` probe via `CMD ["/app", "healthcheck"]` once the binary supports it.
