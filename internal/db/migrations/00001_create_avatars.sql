-- +goose Up
-- +goose StatementBegin
-- Статус загрузки файла в S3.
CREATE TYPE upload_status AS ENUM ('uploading', 'uploaded');

-- Статус асинхронной обработки изображения.
-- Значения соответствуют константам domain.Status* в internal/domain/avatar.go.
CREATE TYPE processing_status AS ENUM ('pending', 'processing', 'ready', 'failed');

CREATE TABLE IF NOT EXISTS avatars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id VARCHAR(255) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    mime_type VARCHAR(100) NOT NULL,
    size_bytes BIGINT NOT NULL,
    width INT,
    height INT,
    s3_key VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB NOT NULL DEFAULT '[]',
    upload_status upload_status NOT NULL DEFAULT 'uploading',
    processing_status processing_status NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_avatars_user_id ON avatars (user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_avatars_status ON avatars (upload_status, processing_status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS avatars;
DROP TYPE IF EXISTS processing_status;
DROP TYPE IF EXISTS upload_status;
-- +goose StatementEnd
