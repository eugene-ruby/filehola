//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugene-ruby/filehola/internal/config"
	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/eugene-ruby/filehola/internal/objectstore"
	"github.com/eugene-ruby/filehola/internal/postgres"
	"github.com/lib/pq"
)

func database(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("FILEHOLA_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://filehola:filehola@127.0.0.1:55432/filehola?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "filehola_test_" + strings.ReplaceAll(files.NewUUID(), "-", "")
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+pq.QuoteIdentifier(name)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	u.Path = "/" + name
	db, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		admin.ExecContext(context.Background(), `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`)
		admin.Close()
	})
	return db
}

func TestMigrationsConcurrentStartup(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { results <- postgres.Migrate(ctx, db) }()
	}
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil || version != 1 || dirty {
		t.Fatalf("version=%d dirty=%v err=%v", version, dirty, err)
	}
}

type trackedObjects struct {
	files.Objects
	mu        sync.Mutex
	keys      []string
	deleted   []string
	afterCopy func()
	ready     chan struct{}
	barrier   chan struct{}
}

func (o *trackedObjects) Delete(ctx context.Context, key string) error {
	o.mu.Lock()
	o.deleted = append(o.deleted, key)
	o.mu.Unlock()
	return o.Objects.Delete(ctx, key)
}

func (o *trackedObjects) track(key string) { o.mu.Lock(); o.keys = append(o.keys, key); o.mu.Unlock() }
func (o *trackedObjects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, mime string) error {
	err := o.Objects.Put(ctx, key, body, size, mime)
	o.track(key)
	if o.ready != nil {
		o.ready <- struct{}{}
	}
	if o.barrier != nil {
		<-o.barrier
	}
	return err
}
func (o *trackedObjects) Copy(ctx context.Context, source, dest string) error {
	err := o.Objects.Copy(ctx, source, dest)
	o.track(dest)
	if o.afterCopy != nil {
		o.afterCopy()
	}
	return err
}
func setup(t *testing.T) (*files.Service, *postgres.Repository, *trackedObjects) {
	t.Helper()
	db := database(t)
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	endpoint := testEndpoint()
	objects := objectstore.New(config.Config{S3Endpoint: endpoint, S3Region: "us-east-1", S3Bucket: "filehola", S3AccessKey: "filehola", S3SecretKey: "filehola-local-secret"})
	if err := objects.HeadBucket(context.Background()); err != nil {
		t.Fatal("test bucket unavailable")
	}
	tracked := &trackedObjects{Objects: objects}
	t.Cleanup(func() {
		for _, key := range tracked.keys {
			objects.Delete(context.Background(), key)
		}
	})
	repo := &postgres.Repository{DB: db}
	return &files.Service{Repo: repo, Objects: tracked, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}, repo, tracked
}

func testEndpoint() string {
	endpoint := os.Getenv("FILEHOLA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:59000"
	}
	return endpoint
}
func create(t *testing.T, s *files.Service, directory, name string, source *files.Reference) files.File {
	t.Helper()
	f, err := s.Create(context.Background(), directory, name, "", source)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func replace(t *testing.T, s *files.Service, f files.File, data string) files.File {
	t.Helper()
	next, err := s.Replace(context.Background(), f.UUID, f.Revision, strings.NewReader(data), int64(len(data)), "text/custom")
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func publish(t *testing.T, r *postgres.Repository, f files.File) files.File {
	t.Helper()
	next, err := r.Publish(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
func content(t *testing.T, o files.Objects, f files.File) string {
	t.Helper()
	body, err := o.Get(context.Background(), f.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func rejection(t *testing.T, err error, status int, code string) {
	t.Helper()
	var e *files.Error
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("want %d %s; got %v", status, code, err)
	}
}

func TestLifecycle(t *testing.T) {
	s, r, o := setup(t)
	ctx := context.Background()
	original := create(t, s, "/docs", "README.md", nil)
	if original.Revision != 1 || original.Size != 0 || original.Status != "draft" || content(t, o, original) != "" {
		t.Fatalf("empty draft: %+v", original)
	}
	original = replace(t, s, original, "first content")
	original = publish(t, r, original)
	draft := create(t, s, "/docs", "README.md", files.Ref(original))
	if draft.ObjectKey == original.ObjectKey || draft.UUID == original.UUID || draft.MIME != "text/custom" || content(t, o, draft) != "first content" {
		t.Fatal("source not independently copied")
	}
	draft = replace(t, s, draft, "replacement")
	draft = publish(t, r, draft)
	old, err := r.Get(ctx, original.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "superseded" || old.SupersededBy == nil || *old.SupersededBy != *files.Ref(draft) || content(t, o, old) != "first content" {
		t.Fatalf("supersession: %+v", old)
	}
	found, err := r.Resolve(ctx, "/docs", "README.md")
	if err != nil || found.UUID != draft.UUID {
		t.Fatalf("resolve: %v", err)
	}
	_, err = r.Resolve(ctx, "/docs", "readme.md")
	rejection(t, err, 404, "not_found")
	if err := s.Delete(ctx, draft.UUID); err == nil {
		t.Fatal("publication deleted")
	}
	_, err = s.Replace(ctx, old.UUID, old.Revision, strings.NewReader("no"), 2, "")
	rejection(t, err, 409, "not_writable")
	stale := create(t, s, "/docs", "README.md", files.Ref(draft))
	draft = replace(t, s, draft, "direct update")
	_, err = r.Publish(ctx, stale.UUID)
	rejection(t, err, 409, "source_conflict")
	unchanged, _ := r.Get(ctx, stale.UUID)
	if unchanged.Status != "draft" || unchanged.Revision != 1 {
		t.Fatal("conflicted draft mutated")
	}
	_, err = s.Create(ctx, "/docs", "README.md", "", &files.Reference{UUID: draft.UUID, Revision: draft.Revision - 1})
	rejection(t, err, 409, "stale_source")
	_, err = s.Create(ctx, "/docs", "readme.md", "", nil)
	rejection(t, err, 409, "path_case_conflict")
	duplicate := create(t, s, "/docs", "README.md", nil)
	_, err = r.Publish(ctx, duplicate.UUID)
	rejection(t, err, 409, "source_conflict")
	_, err = s.Create(ctx, "/elsewhere", "README.md", "", files.Ref(draft))
	rejection(t, err, 422, "source_path_mismatch")
	before := draft
	_, err = s.Replace(ctx, draft.UUID, draft.Revision-1, strings.NewReader("bad"), 3, "")
	rejection(t, err, 412, "revision_mismatch")
	after, _ := r.Get(ctx, draft.UUID)
	if after.ObjectKey != before.ObjectKey || after.Revision != before.Revision {
		t.Fatal("failed PUT mutated metadata")
	}
	if err := s.Delete(ctx, duplicate.UUID); err != nil {
		t.Fatal(err)
	}
	_, err = r.Get(ctx, duplicate.UUID)
	rejection(t, err, 404, "not_found")
	if _, err := o.Get(ctx, duplicate.ObjectKey); err == nil {
		t.Fatal("deleted object retained")
	}
}

func TestConcurrentPUTsKeepWinningObject(t *testing.T) {
	s, r, o := setup(t)
	ctx := context.Background()
	f := create(t, s, "/", "race", nil)
	o.ready = make(chan struct{}, 2)
	o.barrier = make(chan struct{})
	results := make(chan error, 2)
	for _, payload := range []string{"candidate-a", "candidate-b"} {
		go func(payload string) {
			_, err := s.Replace(ctx, f.UUID, 1, strings.NewReader(payload), int64(len(payload)), "text/plain")
			results <- err
		}(payload)
	}
	<-o.ready
	<-o.ready
	close(o.barrier)
	success := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else {
			rejection(t, err, 412, "revision_mismatch")
		}
	}
	winner, err := r.Get(ctx, f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	data := content(t, o, winner)
	if success != 1 || winner.Revision != 2 || (data != "candidate-a" && data != "candidate-b") {
		t.Fatalf("PUT race: successes=%d winner=%+v data=%q", success, winner, data)
	}
	for _, key := range o.keys {
		if key != f.ObjectKey && key != winner.ObjectKey {
			if _, err := o.Get(ctx, key); err == nil {
				t.Fatal("losing object not cleaned")
			}
		}
	}
}

func TestConcurrentPublicationAndDraftCase(t *testing.T) {
	s, r, _ := setup(t)
	ctx := context.Background()
	for _, replacement := range []bool{false, true} {
		name := "first"
		var source *files.Reference
		if replacement {
			name = "replacement"
			original := publish(t, r, create(t, s, "/", name, nil))
			source = files.Ref(original)
		}
		a := create(t, s, "/", name, source)
		b := create(t, s, "/", name, source)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, id := range []string{a.UUID, b.UUID} {
			go func(id string) { <-start; _, err := r.Publish(ctx, id); results <- err }(id)
		}
		close(start)
		success := 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				success++
			} else {
				rejection(t, err, 409, "source_conflict")
			}
		}
		if success != 1 {
			t.Fatal("two active publications")
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"CaseRace", "caserace"} {
		go func(name string) { <-start; _, err := s.Create(ctx, "/", name, "", nil); results <- err }(name)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else {
			rejection(t, err, 409, "path_case_conflict")
		}
	}
	if success != 1 {
		t.Fatal("case conflict race accepted")
	}
}

func TestSourceUpdateDuringCopy(t *testing.T) {
	s, r, o := setup(t)
	original := publish(t, r, replace(t, s, create(t, s, "/", "source", nil), "old"))
	o.afterCopy = func() { replace(t, s, original, "new") }
	_, err := s.Create(context.Background(), "/", "source", "", files.Ref(original))
	rejection(t, err, 409, "stale_source")
	var count int
	if err := r.DB.QueryRow(`SELECT count(*) FROM files`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale copy persisted: count=%d err=%v", count, err)
	}
}

func TestPublishedIndexAndDirectories(t *testing.T) {
	s, r, _ := setup(t)
	ctx := context.Background()
	for _, f := range []struct{ dir, name string }{{"/", "root"}, {"/docs", "ADR-001"}, {"/docs/nested", "ADR-002"}, {"/other", "adr-003"}, {"/another", "ADR_004"}, {"/another/deep", "ADR-005"}, {"/docs", "ADR-x"}} {
		publish(t, r, create(t, s, f.dir, f.name, nil))
	}
	create(t, s, "/hidden/child", "ADR-draft", nil)
	page, err := r.List(ctx, postgres.ListQuery{Directory: "/"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "root" {
		t.Fatalf("root index: %+v %v", page, err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := r.List(ctx, postgres.ListQuery{Search: true, Prefix: "ADR", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range page.Items {
			if seen[f.UUID] || !strings.HasPrefix(f.Name, "ADR") || f.Status != "published" || f.Path == "" {
				t.Fatal("bad index")
			}
			seen[f.UUID] = true
		}
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatal("unexpected terminal cursor")
			}
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("global case-sensitive search: %d", len(seen))
	}
	page, err = r.List(ctx, postgres.ListQuery{Search: true, Prefix: "ADR_"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "ADR_004" {
		t.Fatalf("literal underscore: %+v %v", page, err)
	}
	page, err = r.List(ctx, postgres.ListQuery{Search: true, Prefix: "ADR%"})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("wildcard not literal")
	}
	dirs, err := r.Directories(ctx, postgres.ListQuery{Directory: "/", Limit: 2})
	if err != nil || len(dirs.Items) != 2 || !dirs.HasMore || dirs.Items[0].Name != "another" || dirs.Items[1].Name != "docs" {
		t.Fatalf("directories: %+v %v", dirs, err)
	}
	next, err := r.Directories(ctx, postgres.ListQuery{Directory: "/", Limit: 2, Cursor: *dirs.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Path != "/other" || next.HasMore {
		t.Fatalf("directory cursor: %+v %v", next, err)
	}
	children, err := r.Directories(ctx, postgres.ListQuery{Directory: "/docs"})
	if err != nil || len(children.Items) != 1 || children.Items[0].Path != "/docs/nested" {
		t.Fatalf("direct children: %+v %v", children, err)
	}
	_, err = r.Directories(ctx, postgres.ListQuery{Directory: "/docs", Cursor: *dirs.NextCursor})
	rejection(t, err, 422, "invalid_cursor")
}
