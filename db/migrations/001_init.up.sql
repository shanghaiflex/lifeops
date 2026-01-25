CREATE TABLE IF NOT EXISTS health_workouts (
  id TEXT PRIMARY KEY,
  workout_type TEXT NOT NULL,
  start_ts TIMESTAMPTZ NOT NULL,
  end_ts TIMESTAMPTZ NOT NULL,
  duration_minutes DOUBLE PRECISION NOT NULL,
  distance_meters DOUBLE PRECISION,
  calories DOUBLE PRECISION,
  average_heart_rate DOUBLE PRECISION,
  raw_payload JSONB NOT NULL,
  deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS health_sleep (
  id TEXT PRIMARY KEY,
  start_ts TIMESTAMPTZ NOT NULL,
  end_ts TIMESTAMPTZ NOT NULL,
  total_minutes DOUBLE PRECISION NOT NULL,
  rem_minutes DOUBLE PRECISION NOT NULL,
  deep_minutes DOUBLE PRECISION NOT NULL,
  core_minutes DOUBLE PRECISION NOT NULL,
  awake_minutes DOUBLE PRECISION NOT NULL,
  raw_payload JSONB NOT NULL,
  deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS health_metrics (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  start_ts TIMESTAMPTZ NOT NULL,
  end_ts TIMESTAMPTZ NOT NULL,
  value DOUBLE PRECISION NOT NULL,
  unit TEXT NOT NULL,
  raw_payload JSONB NOT NULL,
  deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS finance_raw (
  id BIGSERIAL PRIMARY KEY,
  source_file TEXT NOT NULL,
  row_num INT NOT NULL,
  payload JSONB NOT NULL,
  imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS finance_transactions (
  id BIGSERIAL PRIMARY KEY,
  txn_date DATE NOT NULL,
  amount DOUBLE PRECISION NOT NULL,
  currency TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT,
  merchant TEXT,
  is_subscription BOOLEAN NOT NULL DEFAULT FALSE,
  raw_id BIGINT REFERENCES finance_raw(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS finance_daily_category (
  id BIGSERIAL PRIMARY KEY,
  day DATE NOT NULL,
  category TEXT NOT NULL,
  total_amount DOUBLE PRECISION NOT NULL
);

CREATE TABLE IF NOT EXISTS finance_subscriptions (
  id BIGSERIAL PRIMARY KEY,
  merchant TEXT NOT NULL,
  avg_amount DOUBLE PRECISION NOT NULL,
  last_date DATE NOT NULL,
  count INT NOT NULL
);

CREATE TABLE IF NOT EXISTS chat_messages (
  id BIGSERIAL PRIMARY KEY,
  chat_id BIGINT NOT NULL,
  agent TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_health_workouts_start ON health_workouts(start_ts);
CREATE INDEX IF NOT EXISTS idx_health_sleep_start ON health_sleep(start_ts);
CREATE INDEX IF NOT EXISTS idx_health_metrics_start ON health_metrics(start_ts);
CREATE INDEX IF NOT EXISTS idx_finance_transactions_date ON finance_transactions(txn_date);
CREATE INDEX IF NOT EXISTS idx_chat_messages_chat_agent ON chat_messages(chat_id, agent);
