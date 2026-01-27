package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

type NutritionEntry struct {
	ID           int64
	ChatID       int64
	MessageTS    time.Time
	OriginalText string
	Summary      string
	Calories     *float64
	ProteinGrams *float64
	CarbsGrams   *float64
	FatGrams     *float64
	PhotoFileID  string
	Raw          json.RawMessage
	CreatedAt    time.Time
}

type CalendarEvent struct {
	ID              string
	CalendarID      string
	EventUID        string
	Title           string
	Description     string
	Location        string
	Start           time.Time
	End             time.Time
	AllDay          bool
	SourceUpdatedAt *time.Time
	Raw             json.RawMessage
}

func (s *Store) InsertNutritionEntry(ctx context.Context, entry NutritionEntry) (int64, error) {
	if entry.Raw == nil {
		entry.Raw = json.RawMessage(`{}`)
	}
	var id int64
	var created time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO nutrition_entries
			(chat_id, message_ts, original_text, summary, calories, protein_g, carbs_g, fat_g, photo_file_id, raw_payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at
	`, entry.ChatID, entry.MessageTS, entry.OriginalText, entry.Summary, entry.Calories, entry.ProteinGrams, entry.CarbsGrams, entry.FatGrams, nullIfEmpty(entry.PhotoFileID), entry.Raw).Scan(&id, &created)
	if err != nil {
		return 0, fmt.Errorf("insert nutrition entry: %w", err)
	}
	entry.ID = id
	entry.CreatedAt = created
	return id, nil
}

func (s *Store) RecentNutritionEntries(ctx context.Context, chatID int64, since time.Time, limit int) ([]NutritionEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, chat_id, message_ts, original_text, summary, calories, protein_g, carbs_g, fat_g, photo_file_id, raw_payload, created_at
		FROM nutrition_entries
		WHERE message_ts >= $1 AND ($2 = 0::bigint OR chat_id = $2)
		ORDER BY message_ts DESC
		LIMIT $3
	`, since, chatID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent nutrition entries: %w", err)
	}
	defer rows.Close()
	var out []NutritionEntry
	for rows.Next() {
		var (
			calories sql.NullFloat64
			protein  sql.NullFloat64
			carbs    sql.NullFloat64
			fat      sql.NullFloat64
			photo    sql.NullString
			raw      []byte
			entry    NutritionEntry
		)
		if err := rows.Scan(
			&entry.ID,
			&entry.ChatID,
			&entry.MessageTS,
			&entry.OriginalText,
			&entry.Summary,
			&calories,
			&protein,
			&carbs,
			&fat,
			&photo,
			&raw,
			&entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("recent nutrition scan: %w", err)
		}
		if calories.Valid {
			entry.Calories = &calories.Float64
		}
		if protein.Valid {
			entry.ProteinGrams = &protein.Float64
		}
		if carbs.Valid {
			entry.CarbsGrams = &carbs.Float64
		}
		if fat.Valid {
			entry.FatGrams = &fat.Float64
		}
		if photo.Valid {
			entry.PhotoFileID = photo.String
		}
		if len(raw) > 0 {
			entry.Raw = json.RawMessage(raw)
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

type UpsertStats struct {
	Inserted int
	Updated  int
}

func (s *Store) UpsertWorkouts(ctx context.Context, workouts []Workout) (UpsertStats, error) {
	var stats UpsertStats
	if len(workouts) == 0 {
		return stats, nil
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
			RETURNING (xmax = 0) AS inserted
		`, w.ID, w.WorkoutType, w.Start, w.End, w.DurationMinutes, w.DistanceMeters, w.Calories, w.AverageHeartRate, w.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range workouts {
		var inserted bool
		if err := br.QueryRow().Scan(&inserted); err != nil {
			return stats, fmt.Errorf("upsert workouts: %w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
		}
	}
	return stats, nil
}

func (s *Store) UpsertSleep(ctx context.Context, sleeps []Sleep) (UpsertStats, error) {
	var stats UpsertStats
	if len(sleeps) == 0 {
		return stats, nil
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
			RETURNING (xmax = 0) AS inserted
		`, sl.ID, sl.Start, sl.End, sl.TotalMinutes, sl.RemMinutes, sl.DeepMinutes, sl.CoreMinutes, sl.AwakeMinutes, sl.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range sleeps {
		var inserted bool
		if err := br.QueryRow().Scan(&inserted); err != nil {
			return stats, fmt.Errorf("upsert sleep: %w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
		}
	}
	return stats, nil
}

func (s *Store) UpsertMetrics(ctx context.Context, metrics []Metric) (UpsertStats, error) {
	var stats UpsertStats
	if len(metrics) == 0 {
		return stats, nil
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
			RETURNING (xmax = 0) AS inserted
		`, m.ID, m.Kind, m.Start, m.End, m.Value, m.Unit, m.Raw)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range metrics {
		var inserted bool
		if err := br.QueryRow().Scan(&inserted); err != nil {
			return stats, fmt.Errorf("upsert metrics: %w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
		}
	}
	return stats, nil
}

func (s *Store) UpsertCalendarEvents(ctx context.Context, events []CalendarEvent) (UpsertStats, error) {
	var stats UpsertStats
	if len(events) == 0 {
		return stats, nil
	}
	batch := &pgx.Batch{}
	for _, ev := range events {
		if ev.Raw == nil {
			ev.Raw = json.RawMessage(`{}`)
		}
		batch.Queue(`
			INSERT INTO calendar_events
				(id, calendar_id, event_uid, starts_at, ends_at, all_day, title, description, location, source_updated_at, raw_payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (id) DO UPDATE
			SET calendar_id = EXCLUDED.calendar_id,
				event_uid = EXCLUDED.event_uid,
				starts_at = EXCLUDED.starts_at,
				ends_at = EXCLUDED.ends_at,
				all_day = EXCLUDED.all_day,
				title = EXCLUDED.title,
				description = EXCLUDED.description,
				location = EXCLUDED.location,
				source_updated_at = EXCLUDED.source_updated_at,
				raw_payload = EXCLUDED.raw_payload,
				updated_at = NOW(),
				deleted_at = NULL
			RETURNING (xmax = 0) AS inserted
		`,
			ev.ID,
			ev.CalendarID,
			ev.EventUID,
			ev.Start,
			ev.End,
			ev.AllDay,
			nullIfEmpty(ev.Title),
			nullIfEmpty(ev.Description),
			nullIfEmpty(ev.Location),
			ev.SourceUpdatedAt,
			ev.Raw,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range events {
		var inserted bool
		if err := br.QueryRow().Scan(&inserted); err != nil {
			return stats, fmt.Errorf("upsert calendar events: %w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Updated++
		}
	}
	return stats, nil
}

func (s *Store) RecentWorkouts(ctx context.Context, since time.Time, limit int) ([]Workout, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, workout_type, start_ts, end_ts, duration_minutes, distance_meters, calories, average_heart_rate, raw_payload
		FROM health_workouts
		WHERE start_ts >= $1 AND deleted_at IS NULL
		ORDER BY start_ts DESC
		LIMIT $2
	`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recent workouts: %w", err)
	}
	defer rows.Close()
	var out []Workout
	for rows.Next() {
		var (
			distance sql.NullFloat64
			calories sql.NullFloat64
			heart    sql.NullFloat64
			raw      []byte
			w        Workout
		)
		if err := rows.Scan(&w.ID, &w.WorkoutType, &w.Start, &w.End, &w.DurationMinutes, &distance, &calories, &heart, &raw); err != nil {
			return nil, fmt.Errorf("recent workouts scan: %w", err)
		}
		if distance.Valid {
			w.DistanceMeters = &distance.Float64
		}
		if calories.Valid {
			w.Calories = &calories.Float64
		}
		if heart.Valid {
			w.AverageHeartRate = &heart.Float64
		}
		if len(raw) > 0 {
			w.Raw = json.RawMessage(raw)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) RecentSleep(ctx context.Context, since time.Time, limit int) ([]Sleep, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, start_ts, end_ts, total_minutes, rem_minutes, deep_minutes, core_minutes, awake_minutes, raw_payload
		FROM health_sleep
		WHERE start_ts >= $1 AND deleted_at IS NULL
		ORDER BY start_ts DESC
		LIMIT $2
	`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recent sleep: %w", err)
	}
	defer rows.Close()
	var out []Sleep
	for rows.Next() {
		var (
			raw []byte
			sl  Sleep
		)
		if err := rows.Scan(&sl.ID, &sl.Start, &sl.End, &sl.TotalMinutes, &sl.RemMinutes, &sl.DeepMinutes, &sl.CoreMinutes, &sl.AwakeMinutes, &raw); err != nil {
			return nil, fmt.Errorf("recent sleep scan: %w", err)
		}
		if len(raw) > 0 {
			sl.Raw = json.RawMessage(raw)
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func (s *Store) RecentMetrics(ctx context.Context, since time.Time, limit int, kinds []string) ([]Metric, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, kind, start_ts, end_ts, value, unit, raw_payload
		FROM health_metrics
		WHERE start_ts >= $1 AND deleted_at IS NULL
	`
	args := []any{since}
	argPos := 2
	if len(kinds) > 0 {
		query += fmt.Sprintf(" AND kind = ANY($%d)", argPos)
		args = append(args, kinds)
		argPos++
	}
	query += fmt.Sprintf(" ORDER BY start_ts DESC LIMIT $%d", argPos)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("recent metrics: %w", err)
	}
	defer rows.Close()
	var out []Metric
	for rows.Next() {
		var (
			raw []byte
			m   Metric
		)
		if err := rows.Scan(&m.ID, &m.Kind, &m.Start, &m.End, &m.Value, &m.Unit, &raw); err != nil {
			return nil, fmt.Errorf("recent metrics scan: %w", err)
		}
		if len(raw) > 0 {
			m.Raw = json.RawMessage(raw)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CalendarEventsBetween(ctx context.Context, calendarID string, start, end time.Time, limit int) ([]CalendarEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, calendar_id, event_uid, title, description, location, starts_at, ends_at, all_day, source_updated_at, raw_payload
		FROM calendar_events
		WHERE deleted_at IS NULL
		  AND ends_at >= $1
		  AND starts_at <= $2
	`
	args := []any{start, end}
	argPos := 3
	if strings.TrimSpace(calendarID) != "" {
		query += fmt.Sprintf(" AND calendar_id = $%d", argPos)
		args = append(args, calendarID)
		argPos++
	}
	query += fmt.Sprintf(" ORDER BY starts_at ASC LIMIT $%d", argPos)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("calendar events: %w", err)
	}
	defer rows.Close()
	var out []CalendarEvent
	for rows.Next() {
		var (
			title   sql.NullString
			desc    sql.NullString
			loc     sql.NullString
			raw     []byte
			updated sql.NullTime
			ev      CalendarEvent
		)
		if err := rows.Scan(
			&ev.ID,
			&ev.CalendarID,
			&ev.EventUID,
			&title,
			&desc,
			&loc,
			&ev.Start,
			&ev.End,
			&ev.AllDay,
			&updated,
			&raw,
		); err != nil {
			return nil, fmt.Errorf("calendar events scan: %w", err)
		}
		if title.Valid {
			ev.Title = title.String
		}
		if desc.Valid {
			ev.Description = desc.String
		}
		if loc.Valid {
			ev.Location = loc.String
		}
		if updated.Valid {
			ev.SourceUpdatedAt = &updated.Time
		}
		if len(raw) > 0 {
			ev.Raw = json.RawMessage(raw)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) SoftDelete(ctx context.Context, sampleType string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	table, err := mapSampleTypeToTable(sampleType)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET deleted_at = NOW() WHERE id = ANY($1)", table)
	_, err = s.pool.Exec(ctx, query, ids)
	if err != nil {
		return fmt.Errorf("soft delete %s: %w", table, err)
	}
	return nil
}

func mapSampleTypeToTable(sampleType string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(sampleType))
	if normalized == "" {
		return "", fmt.Errorf("sampleType is required for deletion")
	}
	switch normalized {
	case "workout", "workouts":
		return "health_workouts", nil
	case "sleep":
		return "health_sleep", nil
	case "metric", "metrics":
		return "health_metrics", nil
	default:
		// Treat metrics kinds (resting_heart_rate, hrv_sdnn, steps, active_energy, etc.) as metrics.
		return "health_metrics", nil
	}
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

func nullIfEmpty(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Store) ReassignNutritionChatID(ctx context.Context, from, to int64) (int64, error) {
	if from == 0 || to == 0 {
		return 0, fmt.Errorf("from/to chat ids must be non-zero")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE nutrition_entries
		SET chat_id = $2
		WHERE chat_id = $1
	`, from, to)
	if err != nil {
		return 0, fmt.Errorf("reassign nutrition chat id: %w", err)
	}
	return tag.RowsAffected(), nil
}
