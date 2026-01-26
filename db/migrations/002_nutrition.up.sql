CREATE TABLE IF NOT EXISTS nutrition_entries (
  id BIGSERIAL PRIMARY KEY,
  chat_id BIGINT NOT NULL,
  message_ts TIMESTAMPTZ NOT NULL,
  original_text TEXT NOT NULL,
  summary TEXT NOT NULL,
  calories DOUBLE PRECISION,
  protein_g DOUBLE PRECISION,
  carbs_g DOUBLE PRECISION,
  fat_g DOUBLE PRECISION,
  photo_file_id TEXT,
  raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_nutrition_entries_chat_ts ON nutrition_entries(chat_id, message_ts DESC);
