package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	base := map[string]string{"FILEHOLA_DATABASE_URL": "postgres://user:secret@localhost/db", "FILEHOLA_S3_ENDPOINT": "http://localhost:9000", "FILEHOLA_S3_REGION": "us-east-1", "FILEHOLA_S3_BUCKET": "test", "FILEHOLA_S3_ACCESS_KEY_ID": "key", "FILEHOLA_S3_SECRET_ACCESS_KEY": "secret", "FILEHOLA_ACCESS_TOKEN": "token"}
	c, err := Load(func(k string) string { return base[k] })
	if err != nil || c.ListenAddr != ":8080" {
		t.Fatalf("load: %v", err)
	}
	for key := range base {
		t.Run(key, func(t *testing.T) {
			_, err := Load(func(k string) string {
				if k == key {
					return ""
				}
				return base[k]
			})
			if err == nil {
				t.Fatal("missing config accepted")
			}
		})
	}
	for _, test := range []struct{ key, value string }{
		{"FILEHOLA_DATABASE_URL", "postgres://user:very-secret@%zz/db"}, {"FILEHOLA_DATABASE_URL", "https://user:very-secret@localhost/db"}, {"FILEHOLA_S3_ENDPOINT", "https://user:very-secret@localhost"}, {"FILEHOLA_S3_ENDPOINT", "http://localhost/path"}, {"FILEHOLA_ACCESS_TOKEN", "very-secret\n"}, {"FILEHOLA_LISTEN_ADDR", "no-port"}, {"FILEHOLA_LISTEN_ADDR", ":abc"},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			_, err := Load(func(k string) string {
				if k == test.key {
					return test.value
				}
				return base[k]
			})
			if err == nil || strings.Contains(err.Error(), "very-secret") {
				t.Fatalf("unsafe validation: %v", err)
			}
		})
	}
}
