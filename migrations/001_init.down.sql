DROP TRIGGER IF EXISTS series_updated_at ON series;
DROP TRIGGER IF EXISTS videos_updated_at ON videos;
DROP FUNCTION IF EXISTS update_updated_at;
DROP TABLE IF EXISTS videos;
DROP TYPE IF EXISTS video_status;
DROP TABLE IF EXISTS series;
DROP TABLE IF EXISTS users;
DROP EXTENSION IF EXISTS "pgcrypto";
