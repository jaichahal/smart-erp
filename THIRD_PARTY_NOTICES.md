# Third-party notices

Smart ERP is proprietary (see `LICENSE`). It depends on open-source software used under the
licences below. This file is a stub; the authoritative, per-dependency report is generated.

## Generated report

The `licence` job in `.github/workflows/ci.yml` runs
[go-licenses](https://github.com/google/go-licenses) over `apps/api`:

```sh
cd apps/api
go-licenses check ./... --disallowed_types=forbidden,restricted --ignore github.com/jaichahal/smart-erp
go-licenses report ./... --ignore github.com/jaichahal/smart-erp > ../../dist/third-party-licences.csv
```

Locally: `just licence`. The CSV (module, licence URL, licence type) is uploaded as the
`third-party-licences` CI artifact on every run and must be attached to every release.

Policy (ADR-01, ADR-14, ADR-15 in `docs/spec/02-architecture.md`):

| go-licenses type | Examples | Allowed |
| --- | --- | --- |
| forbidden | GPL-2.0, GPL-3.0, AGPL-3.0 (as a Go dependency) | no, CI fails |
| restricted | LGPL | no, CI fails |
| reciprocal | MPL-2.0 | yes, unmodified use only |
| notice, permissive, unencumbered | Apache-2.0, MIT, BSD, ISC, Unlicense | yes |

Client dependencies (console, Android, iOS) get their own generated reports when those apps have
dependencies; the same policy applies.

## Components that need a note

**River** (`github.com/riverqueue/river`), MPL-2.0. Used unmodified as the transactional job queue
and outbox (ADR-14). go-licenses classifies MPL-2.0 as *reciprocal*; the reciprocal obligation
applies only to modified MPL files, so unmodified use imposes no obligation on the product's own
code. `reciprocal` is therefore allowed explicitly in the CI policy. If River is ever forked or
patched, the modified files must be published under MPL-2.0.

**Zitadel** (`ghcr.io/zitadel/zitadel`), AGPL-3.0 from v3. Run unmodified as a separate service
(user store and factor verification) over its gRPC API (ADR-15). It is never embedded, linked,
patched or redistributed as part of the product; it does not appear in `go.sum`. The API talks to
it over the network, which does not make the product a derivative work. Documented for counsel.

**MinIO** (`minio/minio`), AGPL-3.0. Run unmodified as a separate object-storage service for
attachments, PDFs, snapshots and backup staging. Same posture as Zitadel: network use only, never
embedded, patched or redistributed. The Go client SDK (`github.com/minio/minio-go`) is Apache-2.0
and is the only MinIO code compiled into the product.

**PostgreSQL**, **Valkey** (BSD-3-Clause), **Caddy** (Apache-2.0),
**chromedp/headless-shell** (BSD-3-Clause tooling around Chromium's BSD licence), **Mailpit**
(MIT), **OpenTelemetry Collector** (Apache-2.0): run as services from the compose stack under their
own licences; not compiled into the product.

## Container images

Every image published to `ghcr.io/jaichahal/smart-erp/*` by `.github/workflows/build.yml` carries
a BuildKit SBOM and provenance attestation; an independent syft SPDX SBOM is produced as a CI
artifact. Base image licences are covered by those SBOMs.
