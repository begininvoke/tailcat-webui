package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/migrate"
	_ "github.com/ca-x/tailcat-webui/ent/runtime"

	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib-x/entsqlite"
)

func Open(ctx context.Context, dsn string) (*ent.Client, *sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping SQLite: %w", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB("sqlite3", db)))
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(true)); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("migrate SQLite: %w", err)
	}
	return client, db, nil
}
