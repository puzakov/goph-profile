package telemetry

import (
	"fmt"
	"io"
	"log/slog"
)

// ParseLevel преобразует имя уровня логирования в slog.Level.
// Регистр строгий: допустимы debug, info, warn, error.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
	}
}

// NewLogger создаёт slog-логгер с заданным уровнем и форматом.
// format: json (по умолчанию в сервисе) или text (удобно локально).
func NewLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q (want json or text)", format)
	}
}
