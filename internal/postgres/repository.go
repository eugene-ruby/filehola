package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/lib/pq"
)

type Repository struct{ DB *sql.DB }
type scanner interface{ Scan(...any) error }

const columns = `uuid, revision, status, directory, name, mime, size, object_key, source_uuid, source_revision, superseded_by_uuid, superseded_by_revision, created_at, updated_at`

func scan(row scanner) (files.File, error) {
	var f files.File
	var source, replacement sql.NullString
	var sourceRev, replacementRev sql.NullInt64
	err := row.Scan(&f.UUID, &f.Revision, &f.Status, &f.Directory, &f.Name, &f.MIME, &f.Size, &f.ObjectKey, &source, &sourceRev, &replacement, &replacementRev, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, files.Reject(404, "not_found")
	}
	if err != nil {
		return f, err
	}
	f.Path = files.Path(f.Directory, f.Name)
	if source.Valid {
		f.Source = &files.Reference{UUID: source.String, Revision: sourceRev.Int64}
	}
	if replacement.Valid {
		f.SupersededBy = &files.Reference{UUID: replacement.String, Revision: replacementRev.Int64}
	}
	return f, nil
}
func (r *Repository) Get(ctx context.Context, uuid string) (files.File, error) {
	return scan(r.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE uuid=$1`, uuid))
}
func (r *Repository) Resolve(ctx context.Context, directory, name string) (files.File, error) {
	return scan(r.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE status='published' AND directory=$1 AND name=$2`, directory, name))
}

// An uncertain COMMIT must never cause deletion of an object that might now be live.
type UncertainCommit struct{ Err error }

func (e *UncertainCommit) Error() string    { return "transaction commit outcome unknown" }
func (e *UncertainCommit) Unwrap() error    { return e.Err }
func (e *UncertainCommit) CommitUncertain() {}
func transient(err error) bool {
	var p *pq.Error
	return errors.As(err, &p) && (p.Code == "40001" || p.Code == "40P01" || p.Code == "23505")
}
func (r *Repository) transaction(ctx context.Context, fn func(*sql.Tx) (files.File, error)) (files.File, error) {
	for attempt := 0; attempt < 5; attempt++ {
		tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return files.File{}, err
		}
		f, err := fn(tx)
		if err != nil {
			tx.Rollback()
			if transient(err) {
				continue
			}
			return files.File{}, err
		}
		if err = tx.Commit(); err != nil {
			if transient(err) {
				continue
			}
			return files.File{}, &UncertainCommit{Err: err}
		}
		return f, nil
	}
	return files.File{}, fmt.Errorf("transaction retry limit exceeded")
}

func (r *Repository) Create(ctx context.Context, draft files.File) (files.File, error) {
	return r.transaction(ctx, func(tx *sql.Tx) (files.File, error) {
		if draft.Source != nil {
			source, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE uuid=$1 FOR SHARE`, draft.Source.UUID))
			if err != nil {
				return files.File{}, err
			}
			if source.Revision != draft.Source.Revision {
				return files.File{}, &files.Error{Status: 409, Code: "stale_source", Source: draft.Source, Current: files.Ref(source)}
			}
			if source.Path != files.Path(draft.Directory, draft.Name) {
				return files.File{}, files.Reject(422, "source_path_mismatch")
			}
		}
		var conflict bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM files WHERE lower(directory)=lower($1) AND lower(name)=lower($2) AND (directory<>$1 OR name<>$2))`, draft.Directory, draft.Name).Scan(&conflict)
		if err != nil {
			return files.File{}, err
		}
		if conflict {
			return files.File{}, files.Reject(409, "path_case_conflict")
		}
		var sourceUUID, sourceRevision any
		if draft.Source != nil {
			sourceUUID = draft.Source.UUID
			sourceRevision = draft.Source.Revision
		}
		return scan(tx.QueryRowContext(ctx, `INSERT INTO files(uuid,revision,status,directory,name,mime,size,object_key,source_uuid,source_revision) VALUES($1,1,'draft',$2,$3,$4,$5,$6,$7,$8) RETURNING `+columns, draft.UUID, draft.Directory, draft.Name, draft.MIME, draft.Size, draft.ObjectKey, sourceUUID, sourceRevision))
	})
}

func (r *Repository) Replace(ctx context.Context, uuid string, revision int64, key, mime string, size int64) (files.File, error) {
	return r.transaction(ctx, func(tx *sql.Tx) (files.File, error) {
		f, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE uuid=$1 FOR UPDATE`, uuid))
		if err != nil {
			return files.File{}, err
		}
		if f.Revision != revision {
			return files.File{}, files.Reject(412, "revision_mismatch")
		}
		if f.Status == "superseded" {
			return files.File{}, files.Reject(409, "not_writable")
		}
		return scan(tx.QueryRowContext(ctx, `UPDATE files SET revision=revision+1,object_key=$2,mime=$3,size=$4,updated_at=clock_timestamp() WHERE uuid=$1 AND revision=$5 AND status IN ('draft','published') RETURNING `+columns, uuid, key, mime, size, revision))
	})
}

func (r *Repository) Publish(ctx context.Context, uuid string) (files.File, error) {
	return r.transaction(ctx, func(tx *sql.Tx) (files.File, error) {
		draft, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE uuid=$1 FOR UPDATE`, uuid))
		if err != nil {
			return files.File{}, err
		}
		if draft.Status != "draft" {
			return files.File{}, files.Reject(409, "not_draft")
		}
		current, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE status='published' AND lower(directory)=lower($1) AND lower(name)=lower($2) FOR UPDATE`, draft.Directory, draft.Name))
		var missing *files.Error
		if err != nil && !(errors.As(err, &missing) && missing.Status == 404) {
			return files.File{}, err
		}
		hasCurrent := err == nil
		if (draft.Source == nil && hasCurrent) || (draft.Source != nil && (!hasCurrent || *draft.Source != *files.Ref(current))) {
			conflict := &files.Error{Status: 409, Code: "source_conflict", Source: draft.Source}
			if hasCurrent {
				conflict.Current = files.Ref(current)
			}
			return files.File{}, conflict
		}
		if hasCurrent {
			if _, err := tx.ExecContext(ctx, `UPDATE files SET status='superseded',superseded_by_uuid=$2,superseded_by_revision=$3,updated_at=clock_timestamp() WHERE uuid=$1`, current.UUID, draft.UUID, draft.Revision); err != nil {
				return files.File{}, err
			}
		}
		return scan(tx.QueryRowContext(ctx, `UPDATE files SET status='published',updated_at=clock_timestamp() WHERE uuid=$1 RETURNING `+columns, uuid))
	})
}

func (r *Repository) Delete(ctx context.Context, uuid string) (files.File, error) {
	return r.transaction(ctx, func(tx *sql.Tx) (files.File, error) {
		// RETURNING binds cleanup to the exact object key removed by this atomic
		// status check. A concurrent PUT either commits first and supplies the
		// new key here, or sees the deleted row and cleans only its candidate.
		f, err := scan(tx.QueryRowContext(ctx, `DELETE FROM files WHERE uuid=$1 AND status='draft' RETURNING `+columns, uuid))
		if err == nil {
			return f, nil
		}
		var missing *files.Error
		if !errors.As(err, &missing) || missing.Status != 404 {
			return files.File{}, err
		}
		_, err = scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM files WHERE uuid=$1`, uuid))
		if err == nil {
			return files.File{}, files.Reject(409, "not_draft")
		}
		return files.File{}, err
	})
}
