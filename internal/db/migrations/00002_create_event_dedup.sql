-- +goose Up
-- +goose StatementBegin
-- Идемпотентность обработки событий брокера: message_id обрабатывается один раз.
-- Вставка конфликтующего message_id (ON CONFLICT DO NOTHING) означает, что
-- событие уже было обработано, и его нужно пропустить.
CREATE TABLE IF NOT EXISTS event_dedup (
    message_id UUID PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS event_dedup;
-- +goose StatementEnd
