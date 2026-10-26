// Command openapi validates the maintained contract and probes the HTTP dispatcher.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/eugene-ruby/filehola/internal/httpapi"
	"github.com/getkin/kin-openapi/openapi3"
)

const exampleUUID = "46c6b8ce-6c00-4bd7-8fbe-41e9ea94e015"

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("OpenAPI 3.1 and HTTP routes: OK")
}

func check() error {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("api/openapi.yaml")
	if err != nil {
		return fmt.Errorf("load OpenAPI: %w", err)
	}
	if doc.OpenAPI != "3.1.0" {
		return fmt.Errorf("expected OpenAPI 3.1.0, got %q", doc.OpenAPI)
	}
	// kin-openapi v0.135 validates most of OpenAPI 3.1, but its validator
	// rejects JSON Schema's null type. Preserve the proper 3.1 union in YAML
	// and adapt only these two nullable pagination schemas for validation.
	for _, name := range []string{"FilePage", "DirectoryPage"} {
		page := doc.Components.Schemas[name]
		if page == nil || page.Value == nil || page.Value.Properties["next_cursor"] == nil || page.Value.Properties["next_cursor"].Value == nil {
			return fmt.Errorf("missing %s.next_cursor schema", name)
		}
		cursor := page.Value.Properties["next_cursor"].Value
		if cursor.Type == nil || len(*cursor.Type) != 2 || !cursor.Type.Includes("string") || !cursor.Type.Includes("null") {
			return fmt.Errorf("%s.next_cursor must allow string and null", name)
		}
		stringType := openapi3.Types{"string"}
		cursor.Type = &stringType
		cursor.Nullable = true
	}
	if err := doc.Validate(context.Background()); err != nil {
		return fmt.Errorf("validate OpenAPI: %w", err)
	}

	paths, err := sourcePaths()
	if err != nil {
		return err
	}
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions, http.MethodTrace, http.MethodConnect}
	want := make(map[string]bool)
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			want[method+" "+path] = true
		}
	}
	got := make(map[string]bool)
	api := httpapi.New("route-check-token", httpapi.Routes(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range paths {
		for _, method := range methods {
			actual := strings.ReplaceAll(path, "{uuid}", exampleUUID)
			if path == "/v1/files/resolve" {
				actual += "?path=/docs/ADR-001.md"
			}
			var body io.Reader
			if method == http.MethodPost && path == "/v1/files" {
				body = bytes.NewBufferString(`{"directory":"/","name":"probe.txt"}`)
			}
			req := httptest.NewRequest(method, actual, body)
			req.Header.Set("Authorization", "Bearer route-check-token")
			req.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, req)
			if response.Code != http.StatusNotFound {
				got[method+" "+path] = true
			}
		}
	}
	if len(want) != len(got) {
		return fmt.Errorf("HTTP route mismatch: OpenAPI=%v, implementation=%v", sorted(want), sorted(got))
	}
	for key := range want {
		if !got[key] {
			return fmt.Errorf("HTTP route mismatch: OpenAPI=%v, implementation=%v", sorted(want), sorted(got))
		}
	}
	return nil
}

func sourcePaths() ([]string, error) {
	files, err := os.ReadFile("internal/httpapi/files.go")
	if err != nil {
		return nil, err
	}
	source := string(files)
	start := strings.Index(source, "func (a *fileAPI) ServeHTTP(")
	end := strings.Index(source, "func jsonResponse(")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("cannot locate file HTTP dispatcher")
	}
	selector := source[start:end]
	casePattern := regexp.MustCompile(`case "([^"]+)":`)
	prefixPattern := regexp.MustCompile(`strings\.HasPrefix\(path, "([^"]+)"\)`)
	match := prefixPattern.FindStringSubmatch(selector)
	if len(match) != 2 || !strings.HasSuffix(match[1], "/") {
		return nil, fmt.Errorf("cannot locate UUID route prefix")
	}
	base := strings.TrimSuffix(match[1], "/") + "/{uuid}"
	paths := map[string]bool{base: true}
	for _, matched := range casePattern.FindAllStringSubmatch(selector, -1) {
		if strings.HasPrefix(matched[1], "/") {
			paths[matched[1]] = true
		} else {
			paths[base+"/"+matched[1]] = true
		}
	}
	server, err := os.ReadFile("internal/httpapi/server.go")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(server), `r.URL.Path == "/health"`) {
		return nil, fmt.Errorf("cannot locate health route")
	}
	paths["/health"] = true
	return sorted(paths), nil
}

func sorted(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
