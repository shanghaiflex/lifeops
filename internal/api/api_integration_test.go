//go:build integration

package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"lifeops/internal/health"
	"lifeops/internal/store"
)

func TestIngestWorkoutsStoresRaw(t *testing.T) {
	ctx := context.Background()
	container, dsn := startPostgres(t, ctx)
	defer func() {
		_ = container.Terminate(ctx)
	}()

	runMigrations(t, dsn)

	pg, err := store.New(ctx, dsn)
	require.NoError(t, err)
	defer pg.Close()

	svc := health.NewService(pg)
	server, err := NewServer(svc, "")
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "workouts.json"))
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/v1/ingest/health/workouts", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	server.Router().ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	row := pool.QueryRow(ctx, "SELECT COUNT(*) FROM health_workouts")
	var count int
	require.NoError(t, row.Scan(&count))
	require.Equal(t, 1, count)
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
