package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

type Workout struct {
	ID               string
	WorkoutType      string
	Start            time.Time
	End              time.Time
	DurationMinutes  float64
	DistanceMeters   *float64
	Calories         *float64
	AverageHeartRate *float64
	Raw              json.RawMessage
}

type Sleep struct {
	ID           string
	Start        time.Time
	End          time.Time
	TotalMinutes float64
	RemMinutes   float64
	DeepMinutes  float64
	CoreMinutes  float64
	AwakeMinutes float64
	Raw          json.RawMessage
}

type Metric struct {
	ID    string
	Kind  string
	Start time.Time
	End   time.Time
	Value float64
	Unit  string
	Raw   json.RawMessage
}

func (s *Store) UpsertWorkouts(ctx context.Context, workouts []Workout) error {
	if len(workouts) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, w := range workouts {
		batch.Queue(`
			INSERT INTO health_workouts
				(id, workout_type, start_ts, end_ts, duration_minutes, distance_meters, calories, average_heart_rate, raw_payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO UPDATE
			SET workout_type = EXCLUDED.workout_type,
				start_ts = EXCLUDED.start_ts,
				end_ts = EXCLUDED.end_ts,
				duration_minutes = EXCLUDED.duration_minutes,
				distance_meters = EXCLUDED.distance_meters,
				calories = EXCLUDED.calories,
				average_heart_rate = EXCLUDED.average_heart_rate,
				raw_payload = EXCLUDED.raw_payload,
				deleted_at = NULL
		`, w.ID, w.WorkoutType, w.Start, w.End, w.DurationMinutes, w.DistanceMeters, w.Calories, w.AverageHeartRate, w.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range workouts {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert workouts: %w", err)
		}
	}
	return nil
}

func (s *Store) UpsertSleep(ctx context.Context, sleeps []Sleep) error {
	if len(sleeps) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, sl := range sleeps {
		batch.Queue(`
			INSERT INTO health_sleep
				(id, start_ts, end_ts, total_minutes, rem_minutes, deep_minutes, core_minutes, awake_minutes, raw_payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO UPDATE
			SET start_ts = EXCLUDED.start_ts,
				end_ts = EXCLUDED.end_ts,
				total_minutes = EXCLUDED.total_minutes,
				rem_minutes = EXCLUDED.rem_minutes,
				deep_minutes = EXCLUDED.deep_minutes,
				core_minutes = EXCLUDED.core_minutes,
				awake_minutes = EXCLUDED.awake_minutes,
				raw_payload = EXCLUDED.raw_payload,
				deleted_at = NULL
		`, sl.ID, sl.Start, sl.End, sl.TotalMinutes, sl.RemMinutes, sl.DeepMinutes, sl.CoreMinutes, sl.AwakeMinutes, sl.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range sleeps {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert sleep: %w", err)
		}
	}
	return nil
}

func (s *Store) UpsertMetrics(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, m := range metrics {
		batch.Queue(`
			INSERT INTO health_metrics
				(id, kind, start_ts, end_ts, value, unit, raw_payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO UPDATE
			SET kind = EXCLUDED.kind,
				start_ts = EXCLUDED.start_ts,
				end_ts = EXCLUDED.end_ts,
				value = EXCLUDED.value,
				unit = EXCLUDED.unit,
				raw_payload = EXCLUDED.raw_payload,
				deleted_at = NULL
		`, m.ID, m.Kind, m.Start, m.End, m.Value, m.Unit, m.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range metrics {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("upsert metrics: %w", err)
		}
	}
	return nil
}

func (s *Store) SoftDelete(ctx context.Context, sampleType string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var table string
	switch sampleType {
	case "workout", "workouts":
		table = "health_workouts"
	case "sleep":
		table = "health_sleep"
	case "metric", "metrics":
		table = "health_metrics"
	default:
		return fmt.Errorf("unknown sampleType %q", sampleType)
	}
	query := fmt.Sprintf("UPDATE %s SET deleted_at = NOW() WHERE id = ANY($1)", table)
	_, err := s.pool.Exec(ctx, query, ids)
	if err != nil {
		return fmt.Errorf("soft delete %s: %w", table, err)
	}
	return nil
}

type FinanceRaw struct {
	SourceFile string
	RowNum     int
	Payload    json.RawMessage
}

type FinanceTransaction struct {
	Date        time.Time
	Amount      float64
	Currency    string
	Description string
	Category    string
	Merchant    string
	IsSub       bool
	RawID       int64
}

func (s *Store) InsertFinanceRaw(ctx context.Context, raws []FinanceRaw) ([]int64, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(raws))
	batch := &pgx.Batch{}
	for _, r := range raws {
		batch.Queue(`
			INSERT INTO finance_raw (source_file, row_num, payload)
			VALUES ($1,$2,$3) RETURNING id
		`, r.SourceFile, r.RowNum, r.Payload)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range raws {
		var id int64
		if err := br.QueryRow().Scan(&id); err != nil {
			return nil, fmt.Errorf("insert finance raw: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) InsertFinanceTransactions(ctx context.Context, txs []FinanceTransaction) error {
	if len(txs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, t := range txs {
		batch.Queue(`
			INSERT INTO finance_transactions
				(txn_date, amount, currency, description, category, merchant, is_subscription, raw_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, t.Date, t.Amount, t.Currency, t.Description, t.Category, t.Merchant, t.IsSub, t.RawID)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range txs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert finance transactions: %w", err)
		}
	}
	return nil
}

func (s *Store) RebuildFinanceAggregates(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE finance_daily_category`)
	if err != nil {
		return fmt.Errorf("truncate finance_daily_category: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO finance_daily_category (day, category, total_amount)
		SELECT txn_date, COALESCE(NULLIF(category,''), 'uncategorized') AS category, SUM(amount)
		FROM finance_transactions
		GROUP BY txn_date, COALESCE(NULLIF(category,''), 'uncategorized')
	`)
	if err != nil {
		return fmt.Errorf("insert finance_daily_category: %w", err)
	}
	_, err = s.pool.Exec(ctx, `TRUNCATE finance_subscriptions`)
	if err != nil {
		return fmt.Errorf("truncate finance_subscriptions: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO finance_subscriptions (merchant, avg_amount, last_date, count)
		SELECT COALESCE(NULLIF(merchant,''), 'unknown') AS merchant,
			AVG(amount) AS avg_amount,
			MAX(txn_date) AS last_date,
			COUNT(*) AS count
		FROM finance_transactions
		WHERE is_subscription = TRUE
		GROUP BY COALESCE(NULLIF(merchant,''), 'unknown')
	`)
	if err != nil {
		return fmt.Errorf("insert finance_subscriptions: %w", err)
	}
	return nil
}

func (s *Store) RecentFinanceSummary(ctx context.Context, since time.Time) (string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT category, SUM(total_amount)
		FROM finance_daily_category
		WHERE day >= $1
		GROUP BY category
		ORDER BY SUM(total_amount) DESC
	`, since)
	if err != nil {
		return "", fmt.Errorf("finance summary: %w", err)
	}
	defer rows.Close()
	var summary string
	for rows.Next() {
		var category string
		var total float64
		if err := rows.Scan(&category, &total); err != nil {
			return "", fmt.Errorf("finance summary scan: %w", err)
		}
		summary += fmt.Sprintf("- %s: %.2f\n", category, total)
	}
	return summary, rows.Err()
}

func (s *Store) RecentHealthSummary(ctx context.Context, since time.Time) (string, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(duration_minutes),0), COUNT(*)
		FROM health_workouts
		WHERE start_ts >= $1 AND deleted_at IS NULL
	`, since)
	var totalMinutes float64
	var count int
	if err := row.Scan(&totalMinutes, &count); err != nil {
		return "", fmt.Errorf("health summary: %w", err)
	}
	row = s.pool.QueryRow(ctx, `
		SELECT COALESCE(AVG(total_minutes),0)
		FROM health_sleep
		WHERE start_ts >= $1 AND deleted_at IS NULL
	`, since)
	var avgSleep float64
	if err := row.Scan(&avgSleep); err != nil {
		return "", fmt.Errorf("sleep summary: %w", err)
	}
	return fmt.Sprintf("Workouts: %d sessions, %.1f minutes total. Avg sleep: %.1f minutes.", count, totalMinutes, avgSleep), nil
}

func (s *Store) MetricsSummary(ctx context.Context, since time.Time) (string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, AVG(value)
		FROM health_metrics
		WHERE start_ts >= $1 AND deleted_at IS NULL
		GROUP BY kind
	`, since)
	if err != nil {
		return "", fmt.Errorf("metrics summary: %w", err)
	}
	defer rows.Close()
	var summary string
	for rows.Next() {
		var kind string
		var avg float64
		if err := rows.Scan(&kind, &avg); err != nil {
			return "", fmt.Errorf("metrics summary scan: %w", err)
		}
		summary += fmt.Sprintf("- %s: %.2f\n", kind, avg)
	}
	return summary, rows.Err()
}

func (s *Store) SaveChatMessage(ctx context.Context, chatID int64, agent string, role string, content string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat_messages (chat_id, agent, role, content)
		VALUES ($1,$2,$3,$4)
	`, chatID, agent, role, content)
	if err != nil {
		return fmt.Errorf("save chat message: %w", err)
	}
	return nil
}

func (s *Store) ChatHistory(ctx context.Context, chatID int64, agent string, limit int) ([]ChatMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role, content
		FROM chat_messages
		WHERE chat_id = $1 AND agent = $2
		ORDER BY id DESC
		LIMIT $3
	`, chatID, agent, limit)
	if err != nil {
		return nil, fmt.Errorf("chat history: %w", err)
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		if err := rows.Scan(&msg.Role, &msg.Content); err != nil {
			return nil, fmt.Errorf("chat history scan: %w", err)
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (s *Store) ChatIDsForAgent(ctx context.Context, agent string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT chat_id
		FROM chat_messages
		WHERE agent = $1
		ORDER BY chat_id
	`, agent)
	if err != nil {
		return nil, fmt.Errorf("chat ids for agent: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("chat ids scan: %w", err)
		}
		out = append(out, chatID)
	}
	return out, rows.Err()
}

type ChatMessage struct {
	Role    string
	Content string
}
