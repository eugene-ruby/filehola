package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/eugene-ruby/filehola/internal/files"
)

type fakeRepo struct {
	mu      sync.Mutex
	f       files.File
	err     error
	created int
}

func (r *fakeRepo) Get(context.Context, string) (files.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f, nil
}
func (r *fakeRepo) Create(_ context.Context, f files.File) (files.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created++
	if r.err != nil {
		return files.File{}, r.err
	}
	r.f = f
	return f, nil
}
func (r *fakeRepo) Replace(_ context.Context, _ string, rev int64, key, mime string, size int64) (files.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return files.File{}, r.err
	}
	if rev != r.f.Revision {
		return files.File{}, files.Reject(412, "revision_mismatch")
	}
	r.f.Revision++
	r.f.ObjectKey = key
	r.f.MIME = mime
	r.f.Size = size
	return r.f, nil
}
func (r *fakeRepo) Publish(context.Context, string) (files.File, error) { panic("unused") }
func (r *fakeRepo) Delete(context.Context, string) (files.File, error)  { return r.f, r.err }

type memoryObjects struct {
	mu                sync.Mutex
	data              map[string][]byte
	putErr, deleteErr error
	deleted           []string
	barrier           chan struct{}
	ready             chan struct{}
}

func (m *memoryObjects) Put(_ context.Context, key string, body io.ReadSeeker, _ int64, _ string) error {
	m.mu.Lock()
	if _, exists := m.data[key]; exists {
		m.mu.Unlock()
		return errors.New("overwrite")
	}
	data, err := io.ReadAll(body)
	m.data[key] = data
	m.mu.Unlock()
	if m.ready != nil {
		m.ready <- struct{}{}
	}
	if m.barrier != nil {
		<-m.barrier
	}
	if m.putErr != nil {
		return m.putErr
	}
	return err
}
func (m *memoryObjects) Copy(_ context.Context, source, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[dest] = append([]byte(nil), m.data[source]...)
	return m.putErr
}
func (m *memoryObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return io.NopCloser(bytes.NewReader(m.data[key])), nil
}
func (m *memoryObjects) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, key)
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.data, key)
	return nil
}
func setup() (*files.Service, *fakeRepo, *memoryObjects, *bytes.Buffer) {
	uuid := files.NewUUID()
	repo := &fakeRepo{f: files.File{UUID: uuid, Revision: 1, Status: "draft", Directory: "/", Name: "a", Path: "/a", MIME: files.DefaultMIME, ObjectKey: "original"}}
	objects := &memoryObjects{data: map[string][]byte{"original": []byte("old")}}
	var logs bytes.Buffer
	return &files.Service{Repo: repo, Objects: objects, Log: slog.New(slog.NewJSONHandler(&logs, nil))}, repo, objects, &logs
}

type unknownCommit struct{}

func (unknownCommit) Error() string    { return "secret database error" }
func (unknownCommit) CommitUncertain() {}

func TestReplacePartialFailures(t *testing.T) {
	for _, scenario := range []string{"upload", "database", "cleanup", "commit"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, o, logs := setup()
			secret := errors.New("secret token DSN")
			switch scenario {
			case "upload":
				o.putErr = secret
			case "database":
				r.err = secret
			case "cleanup":
				r.err = secret
				o.deleteErr = secret
			case "commit":
				r.err = unknownCommit{}
			}
			_, err := s.Replace(context.Background(), r.f.UUID, 1, strings.NewReader("new"), 3, "text/plain")
			if err == nil || r.f.Revision != 1 || r.f.ObjectKey != "original" {
				t.Fatalf("mutation on failure: %+v %v", r.f, err)
			}
			if scenario == "commit" {
				if len(o.deleted) != 0 || len(o.data) != 2 {
					t.Fatal("uncertain candidate deleted")
				}
			} else if len(o.deleted) != 1 {
				t.Fatal("cleanup not attempted")
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("secret logged")
			}
			if string(o.data["original"]) != "old" {
				t.Fatal("old content lost")
			}
		})
	}
}
func TestReplaceConcurrentAttempts(t *testing.T) {
	s, r, o, _ := setup()
	o.barrier = make(chan struct{})
	o.ready = make(chan struct{}, 2)
	results := make(chan error, 2)
	uuid := r.f.UUID
	for _, payload := range []string{"winner-a", "winner-b"} {
		go func(payload string) {
			_, err := s.Replace(context.Background(), uuid, 1, strings.NewReader(payload), int64(len(payload)), "text/plain")
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
			var e *files.Error
			if !errors.As(err, &e) || e.Status != 412 {
				t.Fatalf("loser: %v", err)
			}
		}
	}
	if success != 1 || r.f.Revision != 2 || len(o.data) != 2 || len(o.deleted) != 1 {
		t.Fatalf("race invariant: successes=%d file=%+v objects=%d", success, r.f, len(o.data))
	}
	if !strings.HasPrefix(r.f.ObjectKey, "files/"+r.f.UUID[:2]+"/"+r.f.UUID+"/2/") {
		t.Fatal("invalid key")
	}
	if string(o.data[r.f.ObjectKey]) != "winner-a" && string(o.data[r.f.ObjectKey]) != "winner-b" {
		t.Fatal("winner corrupted")
	}
}
func TestCreateSourceChecksAndCopy(t *testing.T) {
	s, r, o, _ := setup()
	r.f.Size = 3
	f, err := s.Create(context.Background(), "/", "a", "", files.Ref(r.f))
	if err != nil || f.UUID == r.f.Source.UUID || f.ObjectKey == "original" || string(o.data[f.ObjectKey]) != "old" || f.Revision != 1 {
		t.Fatalf("copy: %+v %v", f, err)
	}
	for _, test := range []struct {
		revision int64
		name     string
		status   int
	}{{9, "a", 409}, {1, "A", 422}} {
		s, r, o, _ := setup()
		_, err := s.Create(context.Background(), "/", test.name, "", &files.Reference{UUID: r.f.UUID, Revision: test.revision})
		var e *files.Error
		if !errors.As(err, &e) || e.Status != test.status || len(o.data) != 1 {
			t.Fatalf("source rejection: %v", err)
		}
	}
}
func TestObjectKeysUnique(t *testing.T) {
	uuid := files.NewUUID()
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		key := files.ObjectKey(uuid, 2)
		if seen[key] || strings.ContainsAny(key, "{}") {
			t.Fatal("bad object key")
		}
		seen[key] = true
	}
}
