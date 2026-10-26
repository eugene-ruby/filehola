package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL, S3Endpoint, S3Region, S3Bucket       string
	S3AccessKey, S3SecretKey, AccessToken, ListenAddr string
}

// Load never includes configuration values in errors: even parse errors can contain passwords.
func Load(getenv func(string) string) (Config, error) {
	c := Config{DatabaseURL: getenv("FILEHOLA_DATABASE_URL"), S3Endpoint: getenv("FILEHOLA_S3_ENDPOINT"), S3Region: getenv("FILEHOLA_S3_REGION"), S3Bucket: getenv("FILEHOLA_S3_BUCKET"), S3AccessKey: getenv("FILEHOLA_S3_ACCESS_KEY_ID"), S3SecretKey: getenv("FILEHOLA_S3_SECRET_ACCESS_KEY"), AccessToken: getenv("FILEHOLA_ACCESS_TOKEN"), ListenAddr: getenv("FILEHOLA_LISTEN_ADDR")}
	for _, v := range []struct{ name, value string }{
		{"FILEHOLA_DATABASE_URL", c.DatabaseURL}, {"FILEHOLA_S3_ENDPOINT", c.S3Endpoint}, {"FILEHOLA_S3_REGION", c.S3Region}, {"FILEHOLA_S3_BUCKET", c.S3Bucket}, {"FILEHOLA_S3_ACCESS_KEY_ID", c.S3AccessKey}, {"FILEHOLA_S3_SECRET_ACCESS_KEY", c.S3SecretKey}, {"FILEHOLA_ACCESS_TOKEN", c.AccessToken},
	} {
		if strings.TrimSpace(v.value) == "" {
			return Config{}, fmt.Errorf("%s is required", v.name)
		}
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Path == "" || u.Path == "/" {
		return Config{}, fmt.Errorf("invalid FILEHOLA_DATABASE_URL")
	}
	u, err = url.Parse(c.S3Endpoint)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Config{}, fmt.Errorf("invalid FILEHOLA_S3_ENDPOINT")
	}
	if strings.ContainsAny(c.AccessToken, " \t\r\n") {
		return Config{}, fmt.Errorf("invalid FILEHOLA_ACCESS_TOKEN")
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil || port == "" {
		return Config{}, fmt.Errorf("invalid FILEHOLA_LISTEN_ADDR")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Config{}, fmt.Errorf("invalid FILEHOLA_LISTEN_ADDR")
	}
	return c, nil
}
