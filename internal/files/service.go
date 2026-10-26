package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"time"
)

type Repository interface {
	Get(context.Context, string) (File, error)
	Create(context.Context, File) (File, error)
	Replace(context.Context, string, int64, string, string, int64) (File, error)
	Publish(context.Context, string) (File, error)
	Delete(context.Context, string) (File, error)
}

type Service struct {
	Repo    Repository
	Objects Objects
	Log     *slog.Logger
}

// uncertain marks a commit for which deleting the candidate object could lose live data.
type uncertain interface{ CommitUncertain() }

func (s *Service) cleanup(ctx context.Context, key string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.Objects.Delete(cleanupCtx, key); err != nil {
		s.Log.Warn("object cleanup failed", "operation", "delete_orphan")
	}
}
func (s *Service) rollbackObject(ctx context.Context, key string, err error) {
	var unknown uncertain
	if errors.As(err, &unknown) {
		s.Log.Warn("object retained after uncertain commit")
		return
	}
	s.cleanup(ctx, key)
}

func (s *Service) Create(ctx context.Context, directory, name, mime string, source *Reference) (File, error) {
	if mime == "" {
		mime = DefaultMIME
	}
	f := File{UUID: NewUUID(), Revision: 1, Status: "draft", Directory: directory, Name: name, MIME: mime, Source: source}
	f.ObjectKey = ObjectKey(f.UUID, 1)
	if source == nil {
		if err := s.Objects.Put(ctx, f.ObjectKey, bytes.NewReader(nil), 0, mime); err != nil {
			s.cleanup(ctx, f.ObjectKey)
			return File{}, err
		}
	} else {
		original, err := s.Repo.Get(ctx, source.UUID)
		if err != nil {
			return File{}, err
		}
		if original.Revision != source.Revision {
			return File{}, &Error{Status: 409, Code: "stale_source", Source: source, Current: Ref(original)}
		}
		if original.Path != Path(directory, name) {
			return File{}, Reject(422, "source_path_mismatch")
		}
		f.MIME = original.MIME
		f.Size = original.Size
		if err := s.Objects.Copy(ctx, original.ObjectKey, f.ObjectKey); err != nil {
			s.cleanup(ctx, f.ObjectKey)
			return File{}, err
		}
	}
	result, err := s.Repo.Create(ctx, f)
	if err != nil {
		s.rollbackObject(ctx, f.ObjectKey, err)
	}
	return result, err
}

func (s *Service) Replace(ctx context.Context, uuid string, revision int64, body io.ReadSeeker, size int64, mime string) (File, error) {
	f, err := s.Repo.Get(ctx, uuid)
	if err != nil {
		return File{}, err
	}
	if f.Revision != revision {
		return File{}, Reject(412, "revision_mismatch")
	}
	if f.Status == "superseded" {
		return File{}, Reject(409, "not_writable")
	}
	if size > MaxFileSize {
		return File{}, Reject(413, "content_too_large")
	}
	if mime == "" {
		mime = DefaultMIME
	}
	key := ObjectKey(uuid, revision+1)
	if err := s.Objects.Put(ctx, key, body, size, mime); err != nil {
		s.cleanup(ctx, key)
		return File{}, err
	}
	f, err = s.Repo.Replace(ctx, uuid, revision, key, mime, size)
	if err != nil {
		s.rollbackObject(ctx, key, err)
	}
	return f, err
}

func (s *Service) Delete(ctx context.Context, uuid string) error {
	f, err := s.Repo.Delete(ctx, uuid)
	if err != nil {
		return err
	}
	s.cleanup(ctx, f.ObjectKey)
	return nil
}
