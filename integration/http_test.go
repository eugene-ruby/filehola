//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/eugene-ruby/filehola/internal/httpapi"
	"github.com/eugene-ruby/filehola/internal/postgres"
)

const testToken = "Bearer a-private-test-token"

type response struct {
	status int
	header http.Header
	body   []byte
}
type responseResult struct {
	response response
	err      error
}

func request(t *testing.T, client *http.Client, method, address string, body []byte, headers map[string]string) response {
	t.Helper()
	got, err := sendRequest(client, method, address, body, headers)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func sendRequest(client *http.Client, method, address string, body []byte, headers map[string]string) (response, error) {
	r, err := http.NewRequest(method, address, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	resp, err := client.Do(r)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}
func authorized(extra map[string]string) map[string]string {
	h := map[string]string{"Authorization": testToken}
	for k, v := range extra {
		h[k] = v
	}
	return h
}
func expect(t *testing.T, got response, status int, code string) {
	t.Helper()
	if got.status != status {
		t.Fatalf("status=%d want=%d body=%s", got.status, status, got.body)
	}
	if code != "" {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(got.body, &e) != nil || e.Error != code {
			t.Fatalf("error=%q want=%q body=%s", e.Error, code, got.body)
		}
	}
}
func fileFrom(t *testing.T, got response) files.File {
	t.Helper()
	var f files.File
	if err := json.Unmarshal(got.body, &f); err != nil || !files.ValidUUID(f.UUID) {
		t.Fatalf("metadata: %s %v", got.body, err)
	}
	return f
}
func created(t *testing.T, client *http.Client, base, dir, name string, source *files.Reference) files.File {
	t.Helper()
	payload := map[string]any{"directory": dir, "name": name}
	if source != nil {
		payload["source"] = source
	}
	data, _ := json.Marshal(payload)
	got := request(t, client, "POST", base+"/v1/files", data, authorized(map[string]string{"Content-Type": "application/json"}))
	expect(t, got, 201, "")
	return fileFrom(t, got)
}
func put(t *testing.T, client *http.Client, base string, f files.File, payload, mime string) files.File {
	t.Helper()
	got := request(t, client, "PUT", base+"/v1/files/"+f.UUID+"/content", []byte(payload), authorized(map[string]string{"If-Match": fmt.Sprintf(`"%d"`, f.Revision), "Content-Type": mime}))
	expect(t, got, 200, "")
	next := fileFrom(t, got)
	if got.header.Get("ETag") != fmt.Sprintf(`"%d"`, next.Revision) {
		t.Fatal("PUT ETag does not describe UUID revision")
	}
	return next
}
func publishHTTP(t *testing.T, client *http.Client, base string, f files.File) files.File {
	t.Helper()
	got := request(t, client, "POST", base+"/v1/files/"+f.UUID+"/publish", nil, authorized(nil))
	expect(t, got, 200, "")
	next := fileFrom(t, got)
	if got.header.Get("ETag") != fmt.Sprintf(`"%d"`, next.Revision) {
		t.Fatal("publish ETag wrong")
	}
	return next
}
func server(t *testing.T, s *files.Service, r *postgres.Repository) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	s.Log = log
	server := httptest.NewServer(httpapi.New(strings.TrimPrefix(testToken, "Bearer "), httpapi.Routes(s, r, log), log))
	t.Cleanup(server.Close)
	return server, &logs
}

func TestHTTPFileLifecycleAndETags(t *testing.T) {
	s, r, o := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	for _, endpoint := range []struct{ method, path string }{{"POST", "/v1/files"}, {"GET", "/v1/files"}, {"GET", "/v1/files/resolve?path=/docs/a"}, {"GET", "/v1/files/" + files.NewUUID()}, {"GET", "/v1/files/" + files.NewUUID() + "/content"}, {"PUT", "/v1/files/" + files.NewUUID() + "/content"}, {"POST", "/v1/files/" + files.NewUUID() + "/publish"}, {"DELETE", "/v1/files/" + files.NewUUID()}, {"GET", "/v1/directories"}} {
		expect(t, request(t, client, endpoint.method, base+endpoint.path, nil, nil), 401, "unauthenticated")
		expect(t, request(t, client, endpoint.method, base+endpoint.path, nil, map[string]string{"Authorization": "Bearer wrong"}), 401, "unauthenticated")
	}
	health := request(t, client, "GET", base+"/health", nil, nil)
	expect(t, health, 200, "")
	f := created(t, client, base, "/docs", "ADR-001.md", nil)
	initial, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != "draft" || f.Revision != 1 || f.Size != 0 || f.MIME != files.DefaultMIME || f.Path != "/docs/ADR-001.md" {
		t.Fatalf("empty draft: %+v", f)
	}
	meta := request(t, client, "GET", base+"/v1/files/"+f.UUID, nil, authorized(nil))
	expect(t, meta, 200, "")
	if meta.header.Get("ETag") != `"1"` {
		t.Fatal("metadata ETag")
	}
	empty := request(t, client, "GET", base+"/v1/files/"+f.UUID+"/content", nil, authorized(nil))
	expect(t, empty, 200, "")
	if len(empty.body) != 0 || empty.header.Get("Content-Type") != files.DefaultMIME || empty.header.Get("ETag") != `"1"` {
		t.Fatal("empty content")
	}
	contentURL := base + "/v1/files/" + f.UUID + "/content"
	expect(t, request(t, client, "PUT", contentURL, []byte("x"), authorized(nil)), 428, "if_match_required")
	for _, bad := range []string{"1", `W/"1"`, `"0"`, `"01"`, `"2", "3"`} {
		expect(t, request(t, client, "PUT", contentURL, []byte("x"), authorized(map[string]string{"If-Match": bad})), 422, "invalid_if_match")
	}
	expect(t, request(t, client, "PUT", contentURL, []byte("x"), authorized(map[string]string{"If-Match": `"2"`})), 412, "revision_mismatch")
	oversize := make([]byte, files.MaxFileSize+1)
	expect(t, request(t, client, "PUT", contentURL, oversize, authorized(map[string]string{"If-Match": `"1"`})), 413, "content_too_large")
	before, _ := r.Get(context.Background(), f.UUID)
	if before.Revision != 1 || before.ObjectKey != initial.ObjectKey {
		t.Fatal("rejected PUT changed metadata")
	}
	f = put(t, client, base, f, "original", "text/custom")
	if f.Revision != 2 || f.MIME != "text/custom" || f.Size != 8 {
		t.Fatal("PUT metadata")
	}
	get := request(t, client, "GET", contentURL, nil, authorized(nil))
	expect(t, get, 200, "")
	if string(get.body) != "original" || get.header.Get("Content-Type") != "text/custom" || get.header.Get("ETag") != `"2"` {
		t.Fatal("content/ETag")
	}
	f = publishHTTP(t, client, base, f)
	resolved := request(t, client, "GET", base+"/v1/files/resolve?path="+url.QueryEscape(f.Path), nil, authorized(nil))
	expect(t, resolved, 200, "")
	if fileFrom(t, resolved).UUID != f.UUID || resolved.header.Get("ETag") != `"2"` {
		t.Fatal("resolve wrong UUID/ETag")
	}
	expect(t, request(t, client, "GET", base+"/v1/files/resolve?path=%2Fdocs%2Fadr-001.md", nil, authorized(nil)), 404, "not_found")
	draft := created(t, client, base, "/docs", "ADR-001.md", files.Ref(f))
	draftStored, err := r.Get(context.Background(), draft.UUID)
	if err != nil {
		t.Fatal(err)
	}
	formerStored, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if draft.UUID == f.UUID || draft.ObjectKey != "" || draftStored.ObjectKey == formerStored.ObjectKey || draft.MIME != "text/custom" || draft.Size != 8 || content(t, o, draftStored) != "original" {
		t.Fatal("source copy not independent")
	}
	draft = put(t, client, base, draft, "replacement", "text/plain")
	draft = publishHTTP(t, client, base, draft)
	resolved = request(t, client, "GET", base+"/v1/files/resolve?path="+url.QueryEscape(f.Path), nil, authorized(nil))
	expect(t, resolved, 200, "")
	if fileFrom(t, resolved).UUID != draft.UUID || resolved.header.Get("ETag") != `"2"` {
		t.Fatal("new PATH ETag")
	}
	old := request(t, client, "GET", base+"/v1/files/"+f.UUID, nil, authorized(nil))
	expect(t, old, 200, "")
	former := fileFrom(t, old)
	if former.Status != "superseded" || former.SupersededBy == nil || former.SupersededBy.UUID != draft.UUID || old.header.Get("ETag") != `"2"` {
		t.Fatal("old UUID metadata")
	}
	oldContent := request(t, client, "GET", contentURL, nil, authorized(nil))
	expect(t, oldContent, 200, "")
	if string(oldContent.body) != "original" || oldContent.header.Get("ETag") != `"2"` {
		t.Fatal("superseded UUID content changed")
	}
	expect(t, request(t, client, "PUT", contentURL, []byte("x"), authorized(map[string]string{"If-Match": `"2"`})), 409, "not_writable")
	expect(t, request(t, client, "POST", base+"/v1/files/"+draft.UUID+"/publish", nil, authorized(nil)), 409, "not_draft")
	mismatchBody, _ := json.Marshal(map[string]any{"directory": "/elsewhere", "name": "ADR-001.md", "source": files.Ref(draft)})
	expect(t, request(t, client, "POST", base+"/v1/files", mismatchBody, authorized(nil)), 422, "source_path_mismatch")
	stale := created(t, client, base, "/docs", "ADR-001.md", files.Ref(draft))
	draft = put(t, client, base, draft, "direct update", "text/plain")
	staleBody, _ := json.Marshal(map[string]any{"directory": "/docs", "name": "ADR-001.md", "source": files.Reference{UUID: draft.UUID, Revision: draft.Revision - 1}})
	staleCreate := request(t, client, "POST", base+"/v1/files", staleBody, authorized(nil))
	expect(t, staleCreate, 409, "stale_source")
	var staleDetails files.Error
	if json.Unmarshal(staleCreate.body, &staleDetails) != nil || staleDetails.Source == nil || staleDetails.Current == nil || staleDetails.Current.Revision != draft.Revision {
		t.Fatal("stale source detail")
	}
	conflict := request(t, client, "POST", base+"/v1/files/"+stale.UUID+"/publish", nil, authorized(nil))
	expect(t, conflict, 409, "source_conflict")
	var details files.Error
	if json.Unmarshal(conflict.body, &details) != nil || details.Current == nil || details.Source == nil || details.Current.Revision != draft.Revision || details.Source.Revision != stale.Source.Revision {
		t.Fatal("source conflict detail")
	}
	remaining := request(t, client, "GET", base+"/v1/files/"+stale.UUID, nil, authorized(nil))
	expect(t, remaining, 200, "")
	if fileFrom(t, remaining).Status != "draft" {
		t.Fatal("conflicted draft changed")
	}
	newPublication := publishHTTP(t, client, base, created(t, client, base, "/docs", "ADR-001.md", files.Ref(draft)))
	if newPublication.Revision != 1 {
		t.Fatal("new UUID did not start at revision 1")
	}
	resolved = request(t, client, "GET", base+"/v1/files/resolve?path="+url.QueryEscape(newPublication.Path), nil, authorized(nil))
	expect(t, resolved, 200, "")
	if fileFrom(t, resolved).UUID != newPublication.UUID || resolved.header.Get("ETag") != `"1"` {
		t.Fatal("PATH resolution ETag not scoped to replacement UUID")
	}
	formerResponse := request(t, client, "GET", base+"/v1/files/"+draft.UUID, nil, authorized(nil))
	expect(t, formerResponse, 200, "")
	if formerResponse.header.Get("ETag") != `"3"` || fileFrom(t, formerResponse).Status != "superseded" {
		t.Fatal("superseded UUID ETag changed with PATH")
	}
	expect(t, request(t, client, "DELETE", base+"/v1/files/"+draft.UUID, nil, authorized(nil)), 409, "not_draft")
	deleted := request(t, client, "DELETE", base+"/v1/files/"+stale.UUID, nil, authorized(nil))
	expect(t, deleted, 204, "")
	if len(deleted.body) != 0 {
		t.Fatal("DELETE body")
	}
	expect(t, request(t, client, "GET", base+"/v1/files/"+stale.UUID, nil, authorized(nil)), 404, "not_found")
	caseRequest := []byte(`{"directory":"/docs","name":"adr-001.md"}`)
	expect(t, request(t, client, "POST", base+"/v1/files", caseRequest, authorized(nil)), 409, "path_case_conflict")
}

func TestHTTPListingValidationAndPagination(t *testing.T) {
	s, r, _ := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	for _, item := range []struct{ dir, name string }{{"/", "root"}, {"/docs", "ADR-1"}, {"/docs", "ADR-2"}, {"/docs/deep", "ADR-3"}, {"/other", "ADR_4"}} {
		publishHTTP(t, client, base, created(t, client, base, item.dir, item.name, nil))
	}
	created(t, client, base, "/hidden", "ADR-draft", nil)
	root := request(t, client, "GET", base+"/v1/files", nil, authorized(nil))
	expect(t, root, 200, "")
	expect(t, request(t, client, "GET", base+"/v1/files?limit=9999999999999999999999999999999999999999999", nil, authorized(nil)), 200, "")
	var rootPage postgres.Page[files.File]
	if json.Unmarshal(root.body, &rootPage) != nil || len(rootPage.Items) != 1 || rootPage.Items[0].Name != "root" {
		t.Fatal("root listing")
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		address := base + "/v1/files?name=ADR&limit=2"
		if cursor != "" {
			address += "&cursor=" + url.QueryEscape(cursor)
		}
		got := request(t, client, "GET", address, nil, authorized(nil))
		expect(t, got, 200, "")
		var page postgres.Page[files.File]
		if json.Unmarshal(got.body, &page) != nil {
			t.Fatal("page JSON")
		}
		for _, f := range page.Items {
			if seen[f.UUID] || f.Status != "published" || !strings.HasPrefix(f.Name, "ADR") || f.Path == "" {
				t.Fatal("bad paginated entry")
			}
			seen[f.UUID] = true
		}
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatal("terminal cursor")
			}
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("global search count=%d", len(seen))
	}
	for _, test := range []struct{ path, code string }{{"/v1/files?name=", "invalid_query"}, {"/v1/files?name=ADR&directory=/docs", "invalid_query"}, {"/v1/files?directory=/docs/", "invalid_query"}, {"/v1/files?directory=docs", "invalid_query"}, {"/v1/files?limit=0", "invalid_query"}, {"/v1/files?limit=no", "invalid_query"}, {"/v1/files?cursor=bad", "invalid_cursor"}} {
		expect(t, request(t, client, "GET", base+test.path, nil, authorized(nil)), 422, test.code)
	}
	underscore := request(t, client, "GET", base+"/v1/files?name=ADR_", nil, authorized(nil))
	expect(t, underscore, 200, "")
	var page postgres.Page[files.File]
	if json.Unmarshal(underscore.body, &page) != nil || len(page.Items) != 1 || page.Items[0].Name != "ADR_4" {
		t.Fatal("LIKE wildcard was not escaped")
	}
	percent := request(t, client, "GET", base+"/v1/files?name=%25", nil, authorized(nil))
	expect(t, percent, 200, "")
	if json.Unmarshal(percent.body, &page) != nil || len(page.Items) != 0 {
		t.Fatal("literal percent")
	}
	dirs := request(t, client, "GET", base+"/v1/directories?limit=1", nil, authorized(nil))
	expect(t, dirs, 200, "")
	var directoryPage postgres.Page[postgres.Directory]
	if json.Unmarshal(dirs.body, &directoryPage) != nil || len(directoryPage.Items) != 1 || directoryPage.Items[0].Path != "/docs" || !directoryPage.HasMore {
		t.Fatal("directory page")
	}
	directoryCursor := *directoryPage.NextCursor
	next := request(t, client, "GET", base+"/v1/directories?limit=1&cursor="+url.QueryEscape(*directoryPage.NextCursor), nil, authorized(nil))
	expect(t, next, 200, "")
	if json.Unmarshal(next.body, &directoryPage) != nil || len(directoryPage.Items) != 1 || directoryPage.Items[0].Path != "/other" || directoryPage.HasMore {
		t.Fatal("directory next page")
	}
	children := request(t, client, "GET", base+"/v1/directories?directory=/docs", nil, authorized(nil))
	expect(t, children, 200, "")
	if json.Unmarshal(children.body, &directoryPage) != nil || len(directoryPage.Items) != 1 || directoryPage.Items[0].Path != "/docs/deep" {
		t.Fatal("direct child directories")
	}
	expect(t, request(t, client, "GET", base+"/v1/directories?directory=/docs&cursor="+url.QueryEscape(directoryCursor), nil, authorized(nil)), 422, "invalid_cursor")
	for _, invalid := range []string{`{"directory":"/docs/","name":"a"}`, `{"directory":"/","name":".."}`, `{"directory":"/","name":"A","extra":true}`, `{"directory":"docs","name":"a"}`, `{"directory":"/","name":"a"} {}`} {
		expect(t, request(t, client, "POST", base+"/v1/files", []byte(invalid), authorized(nil)), 422, "invalid_input")
	}
	_ = r
}

type secretGetObjects struct{ files.Objects }

func (s secretGetObjects) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("Bearer a-private-test-token postgres://password filehola-local-secret")
}

type secretPutObjects struct{ files.Objects }

func (s secretPutObjects) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	return errors.New("Bearer a-private-test-token postgres://password filehola-local-secret")
}

type secretDeleteObjects struct{ files.Objects }

func (s secretDeleteObjects) Delete(context.Context, string) error {
	return errors.New("Bearer a-private-test-token postgres://password filehola-local-secret")
}
func TestHTTPDependencyFailureSanitizesSecrets(t *testing.T) {
	s, r, o := setup(t)
	srv, logs := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "secret-check", nil)
	s.Objects = secretGetObjects{s.Objects}
	got := request(t, client, "GET", base+"/v1/files/"+f.UUID+"/content", nil, authorized(nil))
	expect(t, got, 500, "internal_error")
	s.Objects = secretPutObjects{o}
	failedPut := request(t, client, "PUT", base+"/v1/files/"+f.UUID+"/content", []byte("new"), authorized(map[string]string{"If-Match": `"1"`}))
	expect(t, failedPut, 500, "internal_error")
	unchanged, err := r.Get(context.Background(), f.UUID)
	if err != nil || unchanged.Revision != 1 || content(t, o, unchanged) != "" {
		t.Fatal("failed upload mutated file")
	}
	for _, secret := range []string{"a-private-test-token", "postgres://password", "filehola-local-secret"} {
		if strings.Contains(logs.String()+string(got.body)+string(failedPut.body), secret) {
			t.Fatalf("secret leaked: %q", secret)
		}
	}
}

func TestHTTPDatabaseFailureAfterS3Upload(t *testing.T) {
	s, r, o := setup(t)
	srv, logs := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "db-failure", nil)
	initial, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.DB.Exec(`CREATE FUNCTION reject_http_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'Bearer a-private-test-token postgres://password'; END $$; CREATE TRIGGER reject_http_write BEFORE UPDATE ON files FOR EACH ROW EXECUTE FUNCTION reject_http_write()`)
	if err != nil {
		t.Fatal(err)
	}
	got := request(t, client, "PUT", base+"/v1/files/"+f.UUID+"/content", []byte("candidate"), authorized(map[string]string{"If-Match": `"1"`}))
	expect(t, got, 500, "internal_error")
	after, err := r.Get(context.Background(), f.UUID)
	if err != nil || after.Revision != initial.Revision || after.ObjectKey != initial.ObjectKey || content(t, o, after) != "" {
		t.Fatal("database rollback did not preserve content")
	}
	o.mu.Lock()
	keys := append([]string(nil), o.keys...)
	deleted := append([]string(nil), o.deleted...)
	o.mu.Unlock()
	if len(keys) != 2 || len(deleted) != 1 || deleted[0] != keys[1] {
		t.Fatalf("candidate cleanup: keys=%v deleted=%v", keys, deleted)
	}
	if _, err := o.Objects.Get(context.Background(), keys[1]); err == nil {
		t.Fatal("orphan candidate still present")
	}
	if strings.Contains(logs.String()+string(got.body), "a-private-test-token") || strings.Contains(logs.String()+string(got.body), "postgres://password") {
		t.Fatal("database failure leaked secret")
	}
}

func TestHTTPDraftDeleteCleanupFailureLeavesOnlyOrphan(t *testing.T) {
	s, r, o := setup(t)
	srv, logs := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "orphan", nil)
	stored, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	s.Objects = secretDeleteObjects{o}
	got := request(t, client, "DELETE", base+"/v1/files/"+f.UUID, nil, authorized(nil))
	expect(t, got, 204, "")
	_, err = r.Get(context.Background(), f.UUID)
	rejection(t, err, 404, "not_found")
	if _, err := o.Objects.Get(context.Background(), stored.ObjectKey); err != nil {
		t.Fatal("expected orphan after cleanup failure")
	}
	if strings.Contains(logs.String(), "a-private-test-token") || strings.Contains(logs.String(), "postgres://password") {
		t.Fatal("cleanup log leaked secret")
	}
}

func TestHTTPAcceptsExactlyMaxFileSize(t *testing.T) {
	s, r, _ := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "max-size", nil)
	payload := bytes.Repeat([]byte("a"), int(files.MaxFileSize))
	got := request(t, client, "PUT", base+"/v1/files/"+f.UUID+"/content", payload, authorized(map[string]string{"If-Match": `"1"`, "Content-Type": "application/x-custom"}))
	expect(t, got, 200, "")
	updated := fileFrom(t, got)
	if updated.Revision != 2 || updated.Size != files.MaxFileSize || updated.MIME != "application/x-custom" {
		t.Fatal("max payload metadata")
	}
	download := request(t, client, "GET", base+"/v1/files/"+f.UUID+"/content", nil, authorized(nil))
	expect(t, download, 200, "")
	if !bytes.Equal(download.body, payload) || download.header.Get("ETag") != `"2"` {
		t.Fatal("max payload data mismatch")
	}
}

func TestHTTPDefaultAndMaximumPageLimits(t *testing.T) {
	s, r, _ := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	for i := 0; i < 101; i++ {
		publishHTTP(t, client, base, created(t, client, base, "/", fmt.Sprintf("page-%03d", i), nil))
	}
	first := request(t, client, "GET", base+"/v1/files", nil, authorized(nil))
	expect(t, first, 200, "")
	var defaultPage postgres.Page[files.File]
	if err := json.Unmarshal(first.body, &defaultPage); err != nil || len(defaultPage.Items) != files.DefaultPageLimit || !defaultPage.HasMore || defaultPage.NextCursor == nil {
		t.Fatalf("default limit: items=%d has_more=%v err=%v", len(defaultPage.Items), defaultPage.HasMore, err)
	}
	wide := request(t, client, "GET", base+"/v1/files?limit=999999999999999999999999999", nil, authorized(nil))
	expect(t, wide, 200, "")
	var capped postgres.Page[files.File]
	if err := json.Unmarshal(wide.body, &capped); err != nil || len(capped.Items) != files.MaxPageLimit || !capped.HasMore || capped.NextCursor == nil {
		t.Fatalf("maximum limit: items=%d has_more=%v err=%v", len(capped.Items), capped.HasMore, err)
	}
	last := request(t, client, "GET", base+"/v1/files?limit=999&cursor="+url.QueryEscape(*capped.NextCursor), nil, authorized(nil))
	expect(t, last, 200, "")
	var terminal postgres.Page[files.File]
	if err := json.Unmarshal(last.body, &terminal); err != nil || len(terminal.Items) != 1 || terminal.HasMore || terminal.NextCursor != nil || terminal.Items[0].UUID == capped.Items[len(capped.Items)-1].UUID {
		t.Fatalf("last page: items=%d has_more=%v err=%v", len(terminal.Items), terminal.HasMore, err)
	}
}

func TestHTTPConcurrentPUTAndDelete(t *testing.T) {
	s, r, o := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "race", nil)
	initial, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	o.ready = make(chan struct{}, 2)
	o.barrier = make(chan struct{})
	results := make(chan responseResult, 2)
	for _, data := range []string{"winner-one", "winner-two"} {
		go func(data string) {
			got, err := sendRequest(client, "PUT", base+"/v1/files/"+f.UUID+"/content", []byte(data), authorized(map[string]string{"If-Match": `"1"`}))
			results <- responseResult{got, err}
		}(data)
	}
	<-o.ready
	<-o.ready
	close(o.barrier)
	success := 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		got := result.response
		if got.status == 200 {
			success++
		} else {
			expect(t, got, 412, "revision_mismatch")
		}
	}
	if success != 1 {
		t.Fatal("two PUTs succeeded")
	}
	winner, err := r.Get(context.Background(), f.UUID)
	if err != nil || winner.Revision != 2 {
		t.Fatalf("winning metadata: %+v %v", winner, err)
	}
	if value := content(t, o, winner); value != "winner-one" && value != "winner-two" {
		t.Fatalf("winner content=%q", value)
	}
	// Force DELETE to commit after a PUT has uploaded, before that PUT updates PostgreSQL.
	o.ready = make(chan struct{}, 1)
	o.barrier = make(chan struct{})
	putResult := make(chan responseResult, 1)
	go func() {
		got, err := sendRequest(client, "PUT", base+"/v1/files/"+f.UUID+"/content", []byte("candidate"), authorized(map[string]string{"If-Match": `"2"`}))
		putResult <- responseResult{got, err}
	}()
	<-o.ready
	deletion := request(t, client, "DELETE", base+"/v1/files/"+f.UUID, nil, authorized(nil))
	expect(t, deletion, 204, "")
	close(o.barrier)
	loser := <-putResult
	if loser.err != nil {
		t.Fatal(loser.err)
	}
	expect(t, loser.response, 404, "not_found")
	_, err = r.Get(context.Background(), f.UUID)
	rejection(t, err, 404, "not_found")
	if _, err := o.Objects.Get(context.Background(), winner.ObjectKey); err == nil {
		t.Fatal("deleted draft content retained")
	}
	o.mu.Lock()
	deleted := append([]string(nil), o.deleted...)
	keys := append([]string(nil), o.keys...)
	o.mu.Unlock()
	if len(deleted) != 3 {
		t.Fatalf("unexpected cleanup keys: %v", deleted)
	}
	for _, key := range keys {
		if key == initial.ObjectKey || key == winner.ObjectKey {
			continue
		}
		if _, err := o.Objects.Get(context.Background(), key); err == nil {
			t.Fatalf("losing candidate not cleaned: key=%s deleted=%v keys=%v", key, deleted, keys)
		}
	}
}

func TestHTTPConcurrentDraftDeletionIsAtomic(t *testing.T) {
	s, r, o := setup(t)
	srv, _ := server(t, s, r)
	client := srv.Client()
	base := srv.URL
	f := created(t, client, base, "/", "delete-race", nil)
	stored, err := r.Get(context.Background(), f.UUID)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan responseResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			got, err := sendRequest(client, "DELETE", base+"/v1/files/"+f.UUID, nil, authorized(nil))
			results <- responseResult{got, err}
		}()
	}
	oneSuccess := 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		got := result.response
		if got.status == 204 {
			oneSuccess++
		} else {
			expect(t, got, 404, "not_found")
		}
	}
	if oneSuccess != 1 {
		t.Fatal("two DELETEs succeeded")
	}
	_, err = r.Get(context.Background(), f.UUID)
	rejection(t, err, 404, "not_found")
	o.mu.Lock()
	deletes := append([]string(nil), o.deleted...)
	o.mu.Unlock()
	if len(deletes) != 1 || deletes[0] != stored.ObjectKey {
		t.Fatalf("cleanup targeted wrong key: %v", deletes)
	}
}
