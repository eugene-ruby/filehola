//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/eugene-ruby/filehola/internal/config"
	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/eugene-ruby/filehola/internal/objectstore"
)

func TestDatabaseFailureAfterUpload(t *testing.T) {
	s, r, o := setup(t)
	ctx := context.Background()
	f := replace(t, s, create(t, s, "/", "failure", nil), "committed")
	_, err := r.DB.Exec(`CREATE FUNCTION reject_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$; CREATE TRIGGER reject_write BEFORE UPDATE ON files FOR EACH ROW EXECUTE FUNCTION reject_write()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Replace(ctx, f.UUID, f.Revision, strings.NewReader("candidate"), 9, "text/plain")
	if err == nil {
		t.Fatal("forced DB failure not returned")
	}
	after, err := r.Get(ctx, f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != f.Revision || after.ObjectKey != f.ObjectKey || content(t, o, after) != "committed" {
		t.Fatal("committed content lost")
	}
	candidate := o.keys[len(o.keys)-1]
	if candidate == f.ObjectKey {
		t.Fatal("key reused")
	}
	if _, err := o.Get(ctx, candidate); err == nil {
		t.Fatal("candidate not cleaned after rollback")
	}
}

func TestBucketStartupCheckAndImmutability(t *testing.T) {
	_, _, o := setup(t)
	ctx := context.Background()
	key := files.ObjectKey(files.NewUUID(), 1)
	o.track(key)
	if err := o.Objects.Put(ctx, key, strings.NewReader("original"), 8, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := o.Objects.Put(ctx, key, strings.NewReader("overwritten"), 11, "text/plain"); err == nil {
		t.Fatal("overwrite accepted")
	}
	if content(t, o, files.File{ObjectKey: key}) != "original" {
		t.Fatal("immutable object changed")
	}
	// Use the same real endpoint as the suite with a bucket which cannot exist.
	endpoint := testEndpoint()
	missing := objectstore.New(config.Config{S3Endpoint: endpoint, S3Region: "us-east-1", S3Bucket: "missing-" + files.NewUUID(), S3AccessKey: "filehola", S3SecretKey: "filehola-local-secret"})
	if err := missing.HeadBucket(ctx); err == nil {
		t.Fatal("missing bucket accepted")
	}
}
