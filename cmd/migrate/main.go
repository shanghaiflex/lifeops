package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "POSTGRES_DSN is required")
		os.Exit(1)
	}
	path := flag.String("path", "db/migrations", "path to migrations")
	cmd := flag.String("cmd", "up", "migration command: up|down|version")
	flag.Parse()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "driver: %v\n", err)
		os.Exit(1)
	}
	m, err := migrate.NewWithDatabaseInstance("file://"+*path, "postgres", driver)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	switch *cmd {
	case "up":
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			fmt.Fprintf(os.Stderr, "migrate up: %v\n", err)
			os.Exit(1)
		}
	case "down":
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			fmt.Fprintf(os.Stderr, "migrate down: %v\n", err)
			os.Exit(1)
		}
	case "version":
		version, dirty, err := m.Version()
		if err != nil {
			fmt.Fprintf(os.Stderr, "version: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%d (dirty=%v)\n", version, dirty)
	default:
		fmt.Fprintf(os.Stderr, "unknown cmd %s\n", *cmd)
		os.Exit(1)
	}
}
