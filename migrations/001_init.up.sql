CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TYPE video_status AS ENUM ('pending', 'processing', 'ready', 'failed');

-- The videos table doubles as the transcoding queue: rows in status 'pending'
-- are up for grabs, workers claim them with FOR UPDATE SKIP LOCKED and prove
-- liveness via heartbeat_at. Stale 'processing' rows are requeued (or failed
-- once attempts exceeds the limit) by the workers themselves.
CREATE TABLE videos (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    status          video_status NOT NULL DEFAULT 'pending',
    progress        SMALLINT     NOT NULL DEFAULT 0,
    original_path   TEXT         NOT NULL,
    duration        FLOAT8,
    width           INT,
    height          INT,
    size_bytes      BIGINT       NOT NULL DEFAULT 0,
    error_message   TEXT,
    segments        JSONB,
    audio_tracks    JSONB,
    subtitle_tracks JSONB,
    storyboard      JSONB,
    attempts        SMALLINT     NOT NULL DEFAULT 0,
    claimed_at      TIMESTAMPTZ,
    heartbeat_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_videos_status     ON videos(status);
CREATE INDEX idx_videos_created_at ON videos(created_at DESC);

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
