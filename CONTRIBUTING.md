# Contributing to Filehola

Thanks for helping improve Filehola. The project is a standalone file storage service; keep application-specific domain concepts outside its API and data model. Before changing behavior, read [the MVP requirements](docs/REQUIREMENTS.md), [the architecture](docs/ARCHITECTURE.md), and [the OpenAPI contract](api/openapi.yaml).

## Prerequisites

- Go 1.24 or newer.
- Docker and Docker Compose for PostgreSQL 17 and MinIO integration tests.
- Available loopback ports `55432` (PostgreSQL), `59000` (MinIO), and `58080` (the Compose app). A host-run Go process defaults to port `8080`.

The first Compose build compiles pinned MinIO and MinIO Client releases from source and can take several minutes. The local development credentials in `compose.yaml` and `.env.example` are examples only.

## Local development

Start PostgreSQL, MinIO, and the development bucket:

```sh
docker compose up -d --wait postgres minio
docker compose run --rm bucket
```

Build the application with `make build`, or run it directly from the host with the local services:

```sh
FILEHOLA_DATABASE_URL='postgres://filehola:filehola@127.0.0.1:55432/filehola?sslmode=disable' \
FILEHOLA_S3_ENDPOINT='http://127.0.0.1:59000' \
FILEHOLA_S3_REGION='us-east-1' \
FILEHOLA_S3_BUCKET='filehola' \
FILEHOLA_S3_ACCESS_KEY_ID='filehola' \
FILEHOLA_S3_SECRET_ACCESS_KEY='filehola-local-secret' \
FILEHOLA_ACCESS_TOKEN='local-development-token' \
go run ./cmd/filehola
```

`go run` does not read `.env` automatically. The `.env.example` hosts `postgres` and `minio` resolve inside Compose; use the loopback endpoints above when running Go on the host. The existing bucket is checked at startup and migrations are applied automatically. Alternatively, `make up` starts the complete Compose stack at `http://127.0.0.1:58080`; `make down` stops it without deleting volumes.

## Tests and checks

Run unit tests and the race detector:

```sh
go test ./...
go test -race ./...
```

With the Compose dependencies running, run the full PostgreSQL, MinIO, and HTTP integration suite:

```sh
go test -race -tags=integration ./... -count=1
```

`make integration` starts the dependencies and runs that integration command. Tests create isolated PostgreSQL databases and distinct S3 objects, then clean up their fixtures. The local PostgreSQL role needs `CREATEDB`. For non-default test services, set `FILEHOLA_TEST_DATABASE_URL` and `FILEHOLA_TEST_S3_ENDPOINT`; these are test-only overrides, not application configuration.

Also run `go vet ./...`, `make check-api`, and `docker compose build filehola` before requesting review. `make check-api` validates OpenAPI 3.1 and checks its documented operations against the HTTP dispatch code. Use `gofmt -w` on changed Go files; CI checks formatting.

## Changes and tests

Keep changes focused and explain the user-visible behavior in the pull request. Add tests for successful operations, rejected inputs, races, and partial failures when changing the corresponding logic. Preserve immutable S3 object keys and the rule that PostgreSQL never points to an object that has not been written. Avoid logging credentials, Bearer tokens, or complete DSNs. Do not add new endpoints or expand MVP behavior as part of documentation or refactoring changes.

Add a new numbered `.up.sql` and `.down.sql` pair in `internal/postgres/migrations/` for a schema change. Do not rewrite a migration that may have run already. Migrations are embedded and applied at startup; down migrations are not run automatically. Verify a clean database, repeat startup, and concurrent startup when migration behavior changes.

`api/openapi.yaml` is the maintained HTTP contract. When changing an approved API behavior, update it, adjust the narrative in `docs/API.md` as needed, and run `make check-api` plus relevant HTTP integration tests. Keep the OpenAPI examples free of real credentials. The Swagger UI development profile reads this spec; no generator rewrites the API implementation.

## Pull requests

Open a pull request against `main` with the problem, the resulting behavior, test commands and results, and any migration or compatibility considerations. Link a related issue when there is one. Review feedback is welcome; no special commit-message convention or contributor agreement is required. For sensitive security findings, follow [SECURITY.md](SECURITY.md) before opening a public discussion.
