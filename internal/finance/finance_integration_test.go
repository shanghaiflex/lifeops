//go:build integration

package finance

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"lifeops/internal/store"
)

func TestFinanceIngestionBuildsAggregates(t *testing.T) {
	ctx := context.Background()
	container, dsn := startPostgres(t, ctx)
	defer func() {
		_ = container.Terminate(ctx)
	}()

	runMigrations(t, dsn)

	pg, err := store.New(ctx, dsn)
	require.NoError(t, err)
	defer pg.Close()

	ingestor := NewIngestor(pg)
	tmpDir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "finance.csv")
	dst := filepath.Join(tmpDir, "finance.csv")
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o644))

	count, err := ingestor.IngestDirectory(ctx, tmpDir)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	summary, err := pg.RecentFinanceSummary(ctx, mustDate("2024-01-01"))
	require.NoError(t, err)
	require.Contains(t, summary, "Food")
}

func mustDate(value string) time.Time {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func startPostgres(t *testing.T, ctx context.Context) (*postgres.PostgresContainer, string) {
	container, err := postgres.RunContainer(ctx,
		postgres.WithDatabase("lifeops"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return container, connStr
}

func runMigrations(t *testing.T, dsn string) {
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	require.NoError(t, err)
	migrationsPath := filepath.Join("..", "..", "db", "migrations")
	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsPath, "postgres", driver)
	require.NoError(t, err)
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err)
	}
}
