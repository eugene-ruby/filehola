package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndAuthentication(t *testing.T) {
	var logs bytes.Buffer
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := New("secret-token", api, slog.New(slog.NewJSONHandler(&logs, nil)))
	for _, test := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/health", "", 200}, {"HEAD", "/health", "", 204}, {"GET", "/v1/files", "", 401}, {"POST", "/v1/files", "Bearer wrong", 401}, {"GET", "/v1/files", "secret-token", 401}, {"GET", "/v1/files", "Bearer secret-token", 204}, {"DELETE", "/v1/files/id", "Bearer secret-token", 204}, {"GET", "/v1/unknown", "", 401},
	} {
		t.Run(test.method+test.path+test.token, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, nil)
			if test.token != "" {
				r.Header.Set("Authorization", test.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if strings.Contains(logs.String(), "secret-token") {
		t.Fatal("token leaked")
	}
}
func TestPanicSanitized(t *testing.T) {
	var logs bytes.Buffer
	h := New("token", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret DSN") }), slog.New(slog.NewJSONHandler(&logs, nil)))
	r := httptest.NewRequest("GET", "/v1/files", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != 500 || strings.Contains(logs.String()+string(body), "secret") {
		t.Fatal("unsafe panic")
	}
}
