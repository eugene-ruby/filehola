package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/eugene-ruby/filehola/internal/postgres"
)

type fileRepository interface {
	Get(context.Context, string) (files.File, error)
	Resolve(context.Context, string, string) (files.File, error)
	List(context.Context, postgres.ListQuery) (postgres.Page[files.File], error)
	Directories(context.Context, postgres.ListQuery) (postgres.Page[postgres.Directory], error)
	Publish(context.Context, string) (files.File, error)
}

type fileAPI struct {
	service *files.Service
	repo    fileRepository
	log     *slog.Logger
}

// Routes serves exactly the public MVP file and directory endpoints.
func Routes(service *files.Service, repo fileRepository, log *slog.Logger) http.Handler {
	return &fileAPI{service: service, repo: repo, log: log}
}

func (a *fileAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch path {
	case "/v1/files":
		switch r.Method {
		case http.MethodPost:
			a.create(w, r)
		case http.MethodGet:
			a.list(w, r)
		default:
			writeError(w, files.Reject(404, "not_found"))
		}
		return
	case "/v1/files/resolve":
		if r.Method == http.MethodGet {
			a.resolve(w, r)
		} else {
			writeError(w, files.Reject(404, "not_found"))
		}
		return
	case "/v1/directories":
		if r.Method == http.MethodGet {
			a.directories(w, r)
		} else {
			writeError(w, files.Reject(404, "not_found"))
		}
		return
	}
	if !strings.HasPrefix(path, "/v1/files/") {
		writeError(w, files.Reject(404, "not_found"))
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/files/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !files.ValidUUID(parts[0]) {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	uuid := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			a.metadata(w, r, uuid)
		case http.MethodDelete:
			a.delete(w, r, uuid)
		default:
			writeError(w, files.Reject(404, "not_found"))
		}
		return
	}
	switch parts[1] {
	case "content":
		switch r.Method {
		case http.MethodGet:
			a.content(w, r, uuid)
		case http.MethodPut:
			a.replace(w, r, uuid)
		default:
			writeError(w, files.Reject(404, "not_found"))
		}
	case "publish":
		if r.Method == http.MethodPost {
			a.publish(w, r, uuid)
		} else {
			writeError(w, files.Reject(404, "not_found"))
		}
	default:
		writeError(w, files.Reject(404, "not_found"))
	}
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err *files.Error) { jsonResponse(w, err.Status, err) }
func (a *fileAPI) fail(w http.ResponseWriter, operation string, err error) {
	var rejection *files.Error
	if errors.As(err, &rejection) {
		writeError(w, rejection)
		return
	}
	a.log.Error("request failed", "operation", operation, "reason", "dependency_failure")
	writeError(w, files.Reject(500, "internal_error"))
}
func etag(w http.ResponseWriter, f files.File) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, f.Revision))
}
func metadataResponse(w http.ResponseWriter, status int, f files.File, includeETag bool) {
	if includeETag {
		etag(w, f)
	}
	jsonResponse(w, status, f)
}

func validDirectory(directory string) bool {
	if directory == "/" {
		return true
	}
	if !strings.HasPrefix(directory, "/") || strings.HasSuffix(directory, "/") {
		return false
	}
	for _, segment := range strings.Split(directory[1:], "/") {
		if !files.ValidName(segment) {
			return false
		}
	}
	return true
}
func splitPath(path string) (string, string, bool) {
	index := strings.LastIndexByte(path, '/')
	if index < 0 || index == len(path)-1 {
		return "", "", false
	}
	directory := path[:index]
	if directory == "" {
		directory = "/"
	}
	name := path[index+1:]
	return directory, name, validDirectory(directory) && files.ValidName(name)
}
func validMIMEHeader(mime string) bool {
	for i := 0; i < len(mime); i++ {
		if mime[i] < 32 || mime[i] == 127 {
			return false
		}
	}
	return true
}

type createRequest struct {
	Directory string           `json:"directory"`
	Name      string           `json:"name"`
	MIME      string           `json:"mime"`
	Source    *files.Reference `json:"source"`
}

func (a *fileAPI) create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || !validDirectory(request.Directory) || !files.ValidName(request.Name) || !validMIMEHeader(request.MIME) || (request.Source != nil && (!files.ValidUUID(request.Source.UUID) || request.Source.Revision <= 0)) {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	f, err := a.service.Create(r.Context(), request.Directory, request.Name, request.MIME, request.Source)
	if err != nil {
		a.fail(w, "create", err)
		return
	}
	metadataResponse(w, 201, f, false)
}

func (a *fileAPI) metadata(w http.ResponseWriter, r *http.Request, uuid string) {
	f, err := a.repo.Get(r.Context(), uuid)
	if err != nil {
		a.fail(w, "metadata", err)
		return
	}
	metadataResponse(w, 200, f, true)
}
func (a *fileAPI) resolve(w http.ResponseWriter, r *http.Request) {
	path, ok := singleQueryValue(r.URL.Query(), "path")
	if !ok {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	directory, name, ok := splitPath(path)
	if !ok {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	f, err := a.repo.Resolve(r.Context(), directory, name)
	if err != nil {
		a.fail(w, "resolve", err)
		return
	}
	metadataResponse(w, 200, f, true)
}
func (a *fileAPI) content(w http.ResponseWriter, r *http.Request, uuid string) {
	f, err := a.repo.Get(r.Context(), uuid)
	if err != nil {
		a.fail(w, "content_metadata", err)
		return
	}
	body, err := a.service.Objects.Get(r.Context(), f.ObjectKey)
	if err != nil {
		a.fail(w, "content_read", err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	etag(w, f)
	w.WriteHeader(200)
	if _, err := io.Copy(w, body); err != nil {
		a.log.Error("content stream failed", "reason", "transfer_failure")
	}
}
func parseIfMatch(values []string) (int64, *files.Error) {
	if len(values) == 0 {
		return 0, files.Reject(428, "if_match_required")
	}
	if len(values) != 1 {
		return 0, files.Reject(422, "invalid_if_match")
	}
	raw := values[0]
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return 0, files.Reject(422, "invalid_if_match")
	}
	inner := raw[1 : len(raw)-1]
	revision, err := strconv.ParseInt(inner, 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != inner {
		return 0, files.Reject(422, "invalid_if_match")
	}
	return revision, nil
}
func (a *fileAPI) replace(w http.ResponseWriter, r *http.Request, uuid string) {
	revision, rejection := parseIfMatch(r.Header.Values("If-Match"))
	if rejection != nil {
		writeError(w, rejection)
		return
	}
	if r.ContentLength > files.MaxFileSize {
		writeError(w, files.Reject(413, "content_too_large"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, files.MaxFileSize+1))
	if err != nil {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	if int64(len(data)) > files.MaxFileSize {
		writeError(w, files.Reject(413, "content_too_large"))
		return
	}
	mime := r.Header.Get("Content-Type")
	if !validMIMEHeader(mime) {
		writeError(w, files.Reject(422, "invalid_input"))
		return
	}
	f, err := a.service.Replace(r.Context(), uuid, revision, bytes.NewReader(data), int64(len(data)), mime)
	if err != nil {
		a.fail(w, "replace", err)
		return
	}
	metadataResponse(w, 200, f, true)
}
func (a *fileAPI) publish(w http.ResponseWriter, r *http.Request, uuid string) {
	f, err := a.repo.Publish(r.Context(), uuid)
	if err != nil {
		a.fail(w, "publish", err)
		return
	}
	metadataResponse(w, 200, f, true)
}
func (a *fileAPI) delete(w http.ResponseWriter, r *http.Request, uuid string) {
	if err := a.service.Delete(r.Context(), uuid); err != nil {
		a.fail(w, "delete", err)
		return
	}
	w.WriteHeader(204)
}

func singleQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	return first(values), ok && len(values) == 1
}
func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
func queryPage(query url.Values) (postgres.ListQuery, *files.Error) {
	var q postgres.ListQuery
	if values, ok := query["directory"]; ok {
		if len(values) != 1 || !validDirectory(values[0]) {
			return q, files.Reject(422, "invalid_query")
		}
		q.Directory = values[0]
	} else {
		q.Directory = "/"
	}
	if values, ok := query["limit"]; ok {
		if len(values) != 1 || values[0] == "" {
			return q, files.Reject(422, "invalid_query")
		}
		for _, ch := range values[0] {
			if ch < '0' || ch > '9' {
				return q, files.Reject(422, "invalid_query")
			}
		}
		var limit uint64
		for _, ch := range values[0] {
			if limit <= files.MaxPageLimit {
				limit = limit*10 + uint64(ch-'0')
			}
		}
		if limit == 0 {
			return q, files.Reject(422, "invalid_query")
		}
		if limit > files.MaxPageLimit {
			limit = files.MaxPageLimit
		}
		q.Limit = int(limit)
	}
	if values, ok := query["cursor"]; ok {
		if len(values) != 1 || values[0] == "" {
			return q, files.Reject(422, "invalid_cursor")
		}
		q.Cursor = values[0]
	}
	return q, nil
}
func (a *fileAPI) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q, rejection := queryPage(query)
	if rejection != nil {
		writeError(w, rejection)
		return
	}
	if values, ok := query["name"]; ok {
		if len(values) != 1 || values[0] == "" {
			writeError(w, files.Reject(422, "invalid_query"))
			return
		}
		if _, exists := query["directory"]; exists {
			writeError(w, files.Reject(422, "invalid_query"))
			return
		}
		q.Search = true
		q.Prefix = values[0]
	}
	page, err := a.repo.List(r.Context(), q)
	if err != nil {
		a.fail(w, "list", err)
		return
	}
	jsonResponse(w, 200, page)
}
func (a *fileAPI) directories(w http.ResponseWriter, r *http.Request) {
	q, rejection := queryPage(r.URL.Query())
	if rejection != nil {
		writeError(w, rejection)
		return
	}
	page, err := a.repo.Directories(r.Context(), q)
	if err != nil {
		a.fail(w, "directories", err)
		return
	}
	jsonResponse(w, 200, page)
}
