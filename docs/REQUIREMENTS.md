# Filehola — MVP requirements

This document describes product scope and lifecycle rules. The maintained contract for HTTP methods, parameters, schemas and status codes is [api/openapi.yaml](../api/openapi.yaml).

## Purpose and scope

Filehola is an independently developed, open-source file storage service. It stores metadata in PostgreSQL and file content in an S3-compatible object store. It exposes an HTTP API. It has no knowledge of consuming applications.

MVP deliberately excludes users, RBAC, OAuth, a public revision-history API, ranged/resumable uploads, unpublish, and recursive directory listing.

## Identity, paths and metadata

- Every file has a permanent UUID and a monotonically increasing integer `revision`, scoped to that UUID. Creation starts at revision 1.
- Metadata includes UUID, revision, status, directory, name, MIME type, byte size, timestamps, optional source `{uuid, revision}`, and, for superseded publications, a `superseded_by` reference.
- The absolute path is derived from directory and name: root `/` + `name`, otherwise `directory + '/' + name`. Directory is absolute and begins with `/`; names match `^[A-Za-z0-9_.-]+$` and cannot equal `.` or `..`. Reject invalid input rather than silently normalizing it.
- PATH resolution is **case-sensitive**. The active published PATH namespace is **case-insensitively unique**: `/docs/README.md` and `/docs/readme.md` cannot both be published.
- Multiple drafts may have exactly the same PATH. Creating a draft whose PATH differs only by case from an existing PATH is rejected with `409 path_case_conflict` (the implementation must enforce this rule without forbidding same-case drafts).
- Virtual directories are inferred from current published file paths; empty directories do not exist.
- Client-provided MIME is preserved, with `application/octet-stream` as default. Any MIME is accepted; do not sniff or infer MIME from filename.

## Lifecycle

### Create draft

`POST /v1/files` always creates a draft with revision 1.

- Without `source`: create an empty file (size 0).
- With `source: {uuid, revision}`: require that this revision is the **current** revision of the referenced file; copy its current content into a separate object belonging to the new UUID. Store the immutable source reference. Stale source revisions return `409`.
- When a source is specified, the new draft PATH must match the source PATH exactly, including case; otherwise `422`.

### Replace content

`PUT /v1/files/{uuid}/content` replaces the complete binary payload and increments the file's current revision by one. It works for drafts and currently published files. `If-Match: "<current revision>"` is required; missing → `428`, stale → `412`. A failed operation must not change current revision or content.

Max payload: 50 MiB (52,428,800 bytes); larger payloads → `413`. No ranged or resumable writes in MVP.

### Publish

`POST /v1/files/{uuid}/publish` publishes a draft atomically:

- If `source` is absent, no active published file may occupy the PATH (case-insensitive comparison); otherwise `409 source_conflict`.
- If `source` exists, the current publication at that PATH must have the exact UUID and current revision recorded in `source`; otherwise `409 source_conflict`. A failed publish leaves the draft unchanged.
- Publishing replaces the active PATH mapping with the new UUID. The prior published file remains readable by UUID and is marked superseded, with `superseded_by` pointing to its replacement.
- A direct content update of a currently published file increments its revision and makes drafts based on its previous revision stale.
- A published UUID remains addressable only at its **current revision**; historical revisions are not exposed through the API.

### Delete

`DELETE /v1/files/{uuid}` is allowed only for drafts (`204`). Published files cannot be deleted (`409`). Unpublish is future work, not MVP.

## Listing and search

- `GET /v1/files` returns **current published files only**, one per active PATH, excluding drafts and superseded files. Returned metadata includes `status: "published"`.
- With no search, list files directly in `directory`, default `/`, non-recursively.
- `?name=ADR` searches **globally**, by case-sensitive filename prefix, irrespective of directory; include absolute PATH in results. Search takes precedence over the default root directory filter.
- Cursor-based pagination: default limit 50, maximum 100; opaque cursor and `has_more`/`next_cursor` fields. The implemented ordering is documented in the OpenAPI contract.
- `GET /v1/directories` lists immediate virtual subdirectories of `directory` (default `/`), based only on current published files. It does not return files or recurse.

## Operational constraints

- HTTP API requires a configured static Bearer token except `/health`.
- `GET /health` returns `200` if the HTTP process is serving requests; it does not query PostgreSQL or S3 on every call.
- At startup validate configuration, connect to PostgreSQL, apply embedded versioned SQL migrations, check S3 bucket with `HeadBucket`, and only then start HTTP serving. Failure logs a safe reason and exits nonzero.
- Secrets (tokens, access keys, passwords, complete DSNs) must never appear in logs.
- Application limits are Go constants, not environment settings.

## Error semantics

Use structured JSON errors with stable `error` codes. In particular: `401` unauthenticated, `404` missing resource, `409` conflicting source/path or forbidden published delete, `412` stale If-Match, `413` oversize content, `422` invalid input, `428` missing If-Match. Source conflicts should include the expected source and current publication when available.

Example:

```json
{"error":"source_conflict","current":{"uuid":"<uuid>","revision":16},"source":{"uuid":"<uuid>","revision":15}}
```
