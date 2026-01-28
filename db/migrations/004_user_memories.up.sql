CREATE TABLE IF NOT EXISTS user_memories (
  id BIGSERIAL PRIMARY KEY,
  content TEXT NOT NULL,
  agent TEXT,  -- null = all agents, or specific agent like 'coach', 'sleep', etc.
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at TIMESTAMPTZ  -- soft delete support
);

CREATE INDEX IF NOT EXISTS idx_user_memories_agent ON user_memories(agent) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_memories_created ON user_memories(created_at DESC) WHERE deleted_at IS NULL;
