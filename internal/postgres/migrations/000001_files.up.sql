CREATE TABLE files (
    uuid uuid PRIMARY KEY,
    revision bigint NOT NULL CHECK (revision > 0),
    status text NOT NULL CHECK (status IN ('draft', 'published', 'superseded')),
    directory text COLLATE "C" NOT NULL,
    name text COLLATE "C" NOT NULL,
    mime text NOT NULL,
    size bigint NOT NULL CHECK (size BETWEEN 0 AND 52428800),
    object_key text NOT NULL UNIQUE,
    source_uuid uuid,
    source_revision bigint,
    superseded_by_uuid uuid,
    superseded_by_revision bigint,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((source_uuid IS NULL) = (source_revision IS NULL)),
    CHECK (source_revision IS NULL OR source_revision > 0),
    CHECK ((status = 'superseded') = (superseded_by_uuid IS NOT NULL)),
    CHECK ((superseded_by_uuid IS NULL) = (superseded_by_revision IS NULL)),
    CHECK (superseded_by_revision IS NULL OR superseded_by_revision > 0)
);
CREATE UNIQUE INDEX idx_files_published_path_unique ON files (lower(directory), lower(name)) WHERE status = 'published';
CREATE INDEX idx_files_path ON files (lower(directory), lower(name));
CREATE INDEX idx_files_published_directory ON files (directory, created_at DESC, uuid DESC) WHERE status = 'published';
CREATE INDEX idx_files_published_created ON files (created_at DESC, uuid DESC) WHERE status = 'published';
CREATE INDEX idx_files_published_name ON files (name text_pattern_ops) WHERE status = 'published';
