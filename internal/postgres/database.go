package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("database configuration rejected")
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("database connection failed")
	}
	return db, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		return errors.New("migration source failed")
	}
	// A dedicated connection lets the migration driver close without closing the application pool.
	conn, err := db.Conn(ctx)
	if err != nil {
		source.Close()
		return errors.New("migration connection failed")
	}
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{})
	if err != nil {
		conn.Close()
		source.Close()
		return errors.New("migration driver failed")
	}
	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		driver.Close()
		source.Close()
		return errors.New("migration setup failed")
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("database migrations failed")
	}
	return nil
}
