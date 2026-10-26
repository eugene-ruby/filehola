package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eugene-ruby/filehola/internal/config"
	"github.com/eugene-ruby/filehola/internal/files"
	"github.com/eugene-ruby/filehola/internal/httpapi"
	"github.com/eugene-ruby/filehola/internal/objectstore"
	"github.com/eugene-ruby/filehola/internal/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("service stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run(ctx context.Context, log *slog.Logger) error {
	c, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	db, err := postgres.Open(startup, c.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := postgres.Migrate(startup, db); err != nil {
		return err
	}
	objects := objectstore.New(c)
	if err := objects.HeadBucket(startup); err != nil {
		return errors.New("S3 bucket check failed")
	}
	repo := &postgres.Repository{DB: db}
	service := &files.Service{Repo: repo, Objects: objects, Log: log}
	api := httpapi.Routes(service, repo, log)
	server := &http.Server{Addr: c.ListenAddr, Handler: httpapi.New(c.AccessToken, api, log), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, ErrorLog: slog.NewLogLogger(slog.NewTextHandler(safeHTTPLog{log}, nil), slog.LevelError)}
	done := make(chan error, 1)
	go func() { log.Info("HTTP serving"); done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP listener failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return errors.New("HTTP shutdown failed")
		}
		return nil
	}
}

// net/http errors can include client-controlled or credential-bearing text.
type safeHTTPLog struct{ log *slog.Logger }

func (s safeHTTPLog) Write(p []byte) (int, error) {
	s.log.Error("HTTP transport error")
	return len(p), nil
}
