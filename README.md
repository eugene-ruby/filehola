# Filehola

### Say "¡Hola!" to your files.

**Filehola is an open-source file service that brings drafts, revisions, and controlled publishing to S3-compatible storage.**

S3 is great at storing files. But what happens when your application needs to edit a document without replacing the published version? Prevent conflicting updates? Keep old publications accessible while making a new one available at the same path?

That's where Filehola comes in.

Built with Go, PostgreSQL, and S3-compatible object storage, Filehola handles the file lifecycle so your application doesn't have to reinvent it.

## Why Filehola?

Storing a file is easy. Managing its lifecycle is where things get interesting.

Imagine a document that needs to be updated:

1. The current version stays published and available.
2. You create a draft based on that version.
3. You make changes without affecting the published file.
4. Filehola checks that nobody has replaced the original in the meantime.
5. You publish the draft, and the path now points to the new file.

The previous publication doesn't disappear. It remains accessible by UUID, while new requests for the same path resolve to the newly published file.

No custom versioning tables, publication flags, or conflict-handling logic scattered throughout your application.

Filehola takes care of that.

## Features

- **Drafts and publishing** — prepare changes privately and publish them when ready.
- **Optimistic concurrency** — revision-based ETags and `If-Match` prevent silent overwrites.
- **Safe replacements** — publish a new file only when its source is still current.
- **Stable file identities** — every file has its own UUID; superseded publications remain readable.
- **Human-friendly paths** — resolve published files by path without exposing S3 object keys.
- **Search and browsing** — find published files and explore virtual directories with cursor pagination.
- **Immutable object storage** — every upload attempt gets a unique S3 object.
- **Simple integration** — HTTP API with Bearer Token authentication.
- **Self-hosted** — runs with PostgreSQL and AWS S3 or compatible storage such as MinIO.

## How it works

Filehola separates **file identity**, **file content**, and **publication**.

Each file has a UUID and a revision. Its content lives in S3, while PostgreSQL tracks metadata and determines which file is currently published at a given path.

A typical workflow looks like this:

```text
Published file
  UUID: AAAA
  Revision: 15
  Path: /docs/guide.md
       |
       | Create draft from AAAA.15
       v
Draft
  UUID: BBBB
  Revision: 1
       |
       | Edit content
       v
Draft
  UUID: BBBB
  Revision: 2
       |
       | Publish
       v
/docs/guide.md --> BBBB.2

AAAA.15 remains readable by UUID.
```

Publishing is conditional: if the source file has changed since the draft was created, Filehola rejects the replacement instead of silently overwriting someone else's work.

File content is stored in unique S3 objects. PostgreSQL only references successfully written content, and the public API exposes the current revision of each UUID rather than historical object versions.

Filehola doesn't manage users, permissions, or your application's business rules. It focuses on one job: **keeping file changes and publication predictable.**

## Quick Start

Requires Docker and Docker Compose. From the repository root:

```sh
docker compose up -d --build --wait filehola
curl -i http://127.0.0.1:58080/health
```

The first build compiles pinned MinIO and MinIO Client source releases and may take several minutes. Compose creates a local development bucket. The app checks that the bucket exists and applies embedded PostgreSQL migrations on startup. The service is at `http://127.0.0.1:58080`; PostgreSQL and MinIO bind to loopback ports `55432` and `59000`. `docker compose down` stops containers and preserves their volumes.

The Compose credentials and token are local development defaults. Replace them before placing the service outside a local workstation. `.env.example` lists the application settings; copying it to `.env` overrides Compose values but does not configure a host-run Go process automatically.

## Docker Images

Release images will be published only to Docker Hub as `docker.io/eugene2ruby/filehola`. Each image supports `linux/amd64` and `linux/arm64`.

```sh
docker pull "docker.io/eugene2ruby/filehola:latest"
```

For a Compose deployment, use the published image in place of a local `build` entry. Provide PostgreSQL, an existing S3 bucket, and all required [configuration](#configuration) values through your deployment's environment or secret manager:

```yaml
services:
  filehola:
    image: docker.io/eugene2ruby/filehola:latest
    ports:
      - "127.0.0.1:58080:8080"
    environment:
      FILEHOLA_DATABASE_URL: ${FILEHOLA_DATABASE_URL:?required}
      FILEHOLA_S3_ENDPOINT: ${FILEHOLA_S3_ENDPOINT:?required}
      FILEHOLA_S3_REGION: ${FILEHOLA_S3_REGION:?required}
      FILEHOLA_S3_BUCKET: ${FILEHOLA_S3_BUCKET:?required}
      FILEHOLA_S3_ACCESS_KEY_ID: ${FILEHOLA_S3_ACCESS_KEY_ID:?required}
      FILEHOLA_S3_SECRET_ACCESS_KEY: ${FILEHOLA_S3_SECRET_ACCESS_KEY:?required}
      FILEHOLA_ACCESS_TOKEN: ${FILEHOLA_ACCESS_TOKEN:?required}
```

For a release tag `vX.Y.Z`, `X.Y.Z` identifies that exact build; the workflow does not rebuild an existing version tag. `X.Y` advances to the newest published patch in that minor line, and `latest` advances to the newest stable version. The workflow reads the version on each existing Docker Hub tag and never moves a floating tag backward. If an existing tag lacks release metadata, publication stops for review. Pin `X.Y.Z` in production so a later release cannot change the image selected by a redeploy.

Maintainers can additionally enforce this at the registry: in the Docker Hub repository's [Tag mutability settings](https://docs.docker.com/docker-hub/repos/manage/hub-images/immutable-tags/), select **Specific tags are immutable** with the rule `^[0-9]+\.[0-9]+\.[0-9]+$`. Leave `X.Y` and `latest` mutable so the workflow can advance them.

## Configuration

| Environment variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `FILEHOLA_DATABASE_URL` | Yes | None in the Go process; Compose supplies a local DSN | PostgreSQL connection URL |
| `FILEHOLA_S3_ENDPOINT` | Yes | None; Compose uses `http://minio:9000` | S3 API URL |
| `FILEHOLA_S3_REGION` | Yes | None; Compose uses `us-east-1` | S3 region |
| `FILEHOLA_S3_BUCKET` | Yes | None; Compose uses `filehola` | Existing bucket |
| `FILEHOLA_S3_ACCESS_KEY_ID` | Yes | None; Compose uses `filehola` | S3 access key |
| `FILEHOLA_S3_SECRET_ACCESS_KEY` | Yes | None; Compose uses a development secret | S3 secret key |
| `FILEHOLA_ACCESS_TOKEN` | Yes | None; Compose uses a development token | Static API Bearer token |
| `FILEHOLA_LISTEN_ADDR` | No | `:8080` | HTTP listen address |

The app does not create an S3 bucket. S3 path-style addressing is used. Host-run Go needs loopback addresses rather than the Compose service names; see [CONTRIBUTING.md](CONTRIBUTING.md).

## API Overview

The [OpenAPI contract](api/openapi.yaml) is the source of truth for request and response formats. [API notes](docs/API.md) explain lifecycle rules.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST`, `GET` | `/v1/files` | Create draft; list or search publications |
| `GET` | `/v1/files/resolve?path=...` | Resolve an exact-case active path |
| `GET`, `DELETE` | `/v1/files/{uuid}` | Read metadata; delete a draft |
| `GET`, `PUT` | `/v1/files/{uuid}/content` | Download or replace complete binary content |
| `POST` | `/v1/files/{uuid}/publish` | Publish a draft |
| `GET` | `/v1/directories` | List immediate virtual directories |
| `GET` | `/health` | Public process health check |

For the local Compose configuration, these requests create and list files. Set the token in your shell without committing it to a file:

```sh
export FILEHOLA_ACCESS_TOKEN='local-development-token'
curl -i -H "Authorization: Bearer $FILEHOLA_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"directory":"/","name":"hello.txt","mime":"text/plain"}' \
  http://127.0.0.1:58080/v1/files
curl -i -H "Authorization: Bearer $FILEHOLA_ACCESS_TOKEN" \
  http://127.0.0.1:58080/v1/files
```

Copy the returned UUID to replace `<uuid>` below. Use the ETag returned by the metadata GET as `If-Match` for the content PUT:

```sh
curl -i -H "Authorization: Bearer $FILEHOLA_ACCESS_TOKEN" \
  http://127.0.0.1:58080/v1/files/<uuid>
curl -i -X PUT -H "Authorization: Bearer $FILEHOLA_ACCESS_TOKEN" \
  -H 'If-Match: "1"' -H 'Content-Type: text/plain' \
  --data-binary 'Hello, Filehola!' \
  http://127.0.0.1:58080/v1/files/<uuid>/content
curl -i -X POST -H "Authorization: Bearer $FILEHOLA_ACCESS_TOKEN" \
  http://127.0.0.1:58080/v1/files/<uuid>/publish
```

Each UUID's ETag contains its own current revision. Complete uploads are limited to 50 MiB.

## OpenAPI / Swagger UI

`api/openapi.yaml` is a maintained OpenAPI 3.1 contract, checked by `make check-api`. For a local static Swagger UI with interactive requests, run:

```sh
docker compose --profile docs up -d --build --wait swagger-ui
```

Open `http://127.0.0.1:58081/`. The optional UI serves the same YAML file and proxies API requests to the Compose app on the same origin. Use **Authorize** to enter a development token in your own browser session. No token is stored in the UI configuration or examples. The `docs` profile is disabled by default and its port binds to loopback.

## Development and Testing

Go 1.25 or newer is required. See [CONTRIBUTING.md](CONTRIBUTING.md) for host-run setup, test fixtures, migrations, and pull requests. The main checks are:

```sh
go test ./...
go vet ./...
make integration
make check-api
docker compose build filehola
```

`make integration` starts PostgreSQL and MinIO, then runs the full integration suite with `-race`. `make test` runs unit tests with `-race`. CI also checks `gofmt`.

## Architecture

The HTTP service manages validation and lifecycle rules, PostgreSQL stores authoritative metadata and revision state, and S3-compatible storage holds immutable binary objects. Embedded SQL migrations run at startup. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for object keys, transaction boundaries, and consistency rules.

## Limitations

This MVP has one static Bearer token, no accounts or RBAC, no tenant isolation, no rate limits, no malware scanning, no partial or resumable uploads, and no public historical-revision endpoint. `/health` does not check PostgreSQL or S3. Pagination is not a snapshot during concurrent changes. The local Compose stack is a development example and does not establish production readiness, availability, or performance guarantees.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md). Ordinary feedback and bugs can be submitted through [GitHub Issues](https://github.com/eugene-ruby/filehola/issues/new).

## Security

Read [SECURITY.md](SECURITY.md) for private vulnerability reporting and deployment considerations. Keep the service and its databases behind appropriate network controls, use TLS at external boundaries, and protect the Bearer token and storage credentials.

## License

Filehola is licensed under the [MIT License](LICENSE). Copyright (c) 2026 Eugene Pervushin.
