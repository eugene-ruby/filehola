# Filehola HTTP API

The maintained MVP HTTP contract is [OpenAPI 3.1](../api/openapi.yaml). It defines every public route, request and response schema, status, authentication rule, query parameter, binary body, and example. Run `make check-api` to validate the specification and compare its operations with the HTTP dispatcher. The optional [Swagger UI](../README.md#openapi--swagger-ui) presents the same file interactively.

## File lifecycle

A client creates a draft at a directory and name, writes its complete binary content with `If-Match`, and publishes it. Every UUID addresses its current revision; historical revisions are not available through HTTP. Reads of metadata and content, successful content PUTs, and publication return an ETag containing the revision of that UUID. An ETag never represents a path-wide revision. A replacement published at an existing path can start at revision 1 even if the superseded UUID has a higher revision.

Creating a draft with `source` copies the source's current content into a distinct immutable S3 object. The draft must keep the source's exact-case path. Publication replaces the active file only if the stored source UUID and revision still match that publication. The old UUID remains readable with `status: "superseded"` and `superseded_by` pointing to the replacement. Multiple drafts can coexist at an exact path. The published namespace is case-insensitively unique, while resolving a path is case-sensitive.

Complete uploads are limited to 50 MiB. A content PUT needs one strong, quoted positive numeric `If-Match` value matching the UUID's current revision. PUT is allowed on drafts and active publications. Filehola preserves the supplied MIME type or uses `application/octet-stream`; it does not inspect the file contents. There are no range, resumable-upload, or revision-history endpoints.

Deleting a draft atomically checks its state in PostgreSQL. After a known commit, Filehola attempts to delete only the exact S3 object returned by the deleted row. Concurrent PUTs use unique object keys; a losing PUT cleans only its own candidate. Cleanup failures can leave internal orphan objects, while uncertain database commits never trigger object deletion.

## Paths and listings

Directories are virtual and inferred from active published files. The root is `/`. A directory path is absolute, has no trailing slash except at root, and consists of slash-separated segments matching `^[A-Za-z0-9_.-]+$` other than `.` and `..`. A name follows the same segment rule. Filehola does not normalize input paths.

File listing without `name` returns direct published children of a directory, defaulting to `/`. A nonempty `name` searches globally by case-sensitive filename prefix; it cannot be combined with an explicit `directory`. An empty `?name=` is `422 invalid_query`. Directory listing returns immediate child directories, without files or recursion. Both listings use opaque, filter-bound cursors and a default page limit of 50, capped at 100. Pagination is not a snapshot during concurrent changes.

## Errors and deployment

Errors are JSON with a stable `error` code. Source conflicts include `source` and `current` references when available. Unexpected dependency failures use a sanitized `internal_error` response. All `/v1` operations require the configured static Bearer token; `GET /health` is public and checks only that the HTTP process is serving. For deployment guidance, see [SECURITY.md](../SECURITY.md).
