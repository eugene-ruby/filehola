package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// New installs process health, authentication and a private API handler.
func New(token string, api http.Handler, log *slog.Logger) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recover() != nil {
				log.Error("request panic")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "internal_error"})
			}
		}()
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			auth := r.Header.Get("Authorization")
			supplied := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
			if !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare(supplied[:], expected[:]) != 1 || len(r.Header.Values("Authorization")) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("WWW-Authenticate", "Bearer")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
				return
			}
		}
		api.ServeHTTP(w, r)
	})
}
