# Filehola — MVP architecture

## Components

- **Go HTTP service**: API, validation, lifecycle orchestration, Bearer-token authentication.
- **PostgreSQL**: authoritative metadata, current revision, object key, publication state and transaction boundaries.
- **S3-compatible object store**: immutable content objects; AWS S3 and MinIO supported through AWS SDK for Go v2, path-style addressing by default.
- **Docker/Compose**: reproducible service deployment and local integration dependencies.

No ORM required. Use versioned SQL migrations with `golang-migrate`, embedded using `go:embed`, automatically applied at startup. Use the migration library's standard PostgreSQL locking. No custom distributed migration locks or automatic down-migrations at startup.

## S3 object key

```
files/{uuid[0:2]}/{uuid}/{revision}/{object_uuid}
```

Example:

```
files/46/46c6b8ce-6c00-4bd7-8fbe-41e9ea94e015/15/934b2c38-45a1-4318-9a68-f87e4fc153b2
```

UUIDs are canonical lowercase strings without braces. The two-character prefix is for manual S3 maintenance, not a filesystem directory or guaranteed performance optimization. Each write attempt generates a fresh `object_uuid`; **never overwrite an S3 object**. PostgreSQL stores the exact key for the current revision. Historical successful objects are internal only and have no public read endpoint.

## Consistency of binary writes

1. Validate UUID, permissions/status, `If-Match`, and size.
2. Upload content to a new, unique S3 key for the candidate next revision.
3. Conditionally update the PostgreSQL metadata and current revision only if the expected revision still matches.
4. If step 3 has definitely rolled back, attempt to delete the newly uploaded S3 object. Cleanup failure may leave an orphan; log safely, without changing the committed file state. If the outcome of `COMMIT` is unknown, retain the object rather than risk deleting committed content.

**Invariant:** PostgreSQL must never point to an object that has not been successfully written to S3. Unique per-attempt keys also prevent concurrent losing requests from overwriting the winning request's content. DB metadata updates and publication state changes must be transactional.

For source-based draft creation, copy the source's current content into a distinct object key under the draft UUID; never alias the source's S3 key. The exact approach (server-side CopyObject where supported, or compatible fallback) is an implementation detail, but source revision checks must remain correct under concurrent updates.

Draft deletion uses a PostgreSQL `DELETE ... WHERE status='draft' RETURNING object_key` inside a transaction. It attempts cleanup only for the returned key after a known commit. A concurrent PUT either commits first, making its new key the one returned by a later successful draft deletion, or loses after deletion and cleans only its own uncommitted candidate. Historical successful objects remain internal.

## Publication and concurrency

Application code validates publication business rules before changing state. PostgreSQL indexes are a final integrity safeguard, not the primary business workflow. The active published PATH namespace has a partial case-insensitive unique index, conceptually:

```sql
CREATE UNIQUE INDEX idx_files_published_path_unique
ON files (lower(directory), lower(name))
WHERE status = 'published';
```

If the implementation represents superseded publications using a separate status, the predicate above is directly applicable. If it uses a different internal representation, preserve the same invariant. A unique-index violation from a race must never result in two active publications. Avoid speculative advisory-lock infrastructure for MVP.

## Configuration

| Environment variable | Meaning |
| --- | --- |
| `FILEHOLA_DATABASE_URL` | PostgreSQL connection DSN |
| `FILEHOLA_S3_ENDPOINT` | S3 API endpoint |
| `FILEHOLA_S3_REGION` | S3 region |
| `FILEHOLA_S3_BUCKET` | Existing bucket name |
| `FILEHOLA_S3_ACCESS_KEY_ID` | S3 access key |
| `FILEHOLA_S3_SECRET_ACCESS_KEY` | S3 secret |
| `FILEHOLA_ACCESS_TOKEN` | Static HTTP Bearer token |
| `FILEHOLA_LISTEN_ADDR` | Listen address, default `:8080` |

The bucket must exist before startup; Filehola does not create it. S3 path-style addressing is the default. Do not introduce configuration flags for application limits in MVP.

Go application constants:

```go
const (
    MaxFileSize       int64 = 50 * 1024 * 1024
    DefaultPageLimit        = 50
    MaxPageLimit            = 100
)
```

## Startup and logging

Startup sequence: validate environment → PostgreSQL connect/ping → embedded migrations → S3 `HeadBucket` → start HTTP server. Failure at any stage: structured JSON log via `log/slog` to stdout/stderr, redacted safe reason, nonzero exit. `/health` is unauthenticated and intentionally only checks the HTTP server's availability.

## Deployment boundary

Filehola is a standalone service. Consuming systems can act as facades, but their identities and domain concepts must not leak into Filehola's API or documentation. Deploy behind private networking; a static Bearer token is not a substitute for network isolation or TLS where traffic crosses trust boundaries.
