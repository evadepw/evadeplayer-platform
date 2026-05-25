CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email      TEXT        NOT NULL UNIQUE,
    password   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO users (id, email, password)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'service@evadeplayer.local',
    'service-key-auth'
)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE series (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_series_user_id    ON series(user_id);
CREATE INDEX idx_series_created_at ON series(created_at DESC);

CREATE TYPE video_status AS ENUM ('pending', 'processing', 'ready', 'failed');

CREATE TABLE videos (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title               TEXT         NOT NULL,
    description         TEXT         NOT NULL DEFAULT '',
    status              video_status NOT NULL DEFAULT 'pending',
    progress            SMALLINT     NOT NULL DEFAULT 0,
    original_path       TEXT         NOT NULL,
    duration            FLOAT,
    width               INT,
    height              INT,
    size_bytes          BIGINT       NOT NULL DEFAULT 0,
    error_message       TEXT,
    series_id           UUID         REFERENCES series(id) ON DELETE SET NULL,
    season_number       INT,
    episode_number      INT,
    version_of          UUID         REFERENCES videos(id) ON DELETE SET NULL,
    version_label       TEXT,
    version_description TEXT,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_videos_user_id    ON videos(user_id);
CREATE INDEX idx_videos_status     ON videos(status);
CREATE INDEX idx_videos_created_at ON videos(created_at DESC);
CREATE INDEX idx_videos_series_id  ON videos(series_id);
CREATE INDEX idx_videos_version_of ON videos(version_of);

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER videos_updated_at
    BEFORE UPDATE ON videos
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER series_updated_at
    BEFORE UPDATE ON series
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
