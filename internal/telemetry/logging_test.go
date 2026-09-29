package telemetry_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"goph-profile/internal/telemetry"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    slog.Level
		wantErr bool
	}{
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "warn", input: "warn", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "пусто", input: "", wantErr: true},
		{name: "неизвестный уровень", input: "trace", wantErr: true},
		// Регистр строгий: значение из окружения должно совпадать точно.
		{name: "верхний регистр", input: "INFO", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := telemetry.ParseLevel(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewLogger_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log, err := telemetry.NewLogger(&buf, "info", "json")
	require.NoError(t, err)

	log.Info("загрузка завершена", "avatar_id", "a1")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, "загрузка завершена", entry["msg"])
	require.Equal(t, "INFO", entry["level"])
	require.Equal(t, "a1", entry["avatar_id"])
}

func TestNewLogger_LevelFiltersLowerRecords(t *testing.T) {
	var buf bytes.Buffer
	log, err := telemetry.NewLogger(&buf, "warn", "json")
	require.NoError(t, err)

	log.Info("не должна попасть в вывод")
	log.Warn("должна попасть")

	require.NotContains(t, buf.String(), "не должна попасть")
	require.Contains(t, buf.String(), "должна попасть")
}

func TestNewLogger_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	log, err := telemetry.NewLogger(&buf, "debug", "text")
	require.NoError(t, err)

	log.Debug("проверка")

	require.Contains(t, buf.String(), "level=DEBUG")
	require.Contains(t, buf.String(), "проверка")
}

func TestNewLogger_InvalidFormat(t *testing.T) {
	_, err := telemetry.NewLogger(&bytes.Buffer{}, "info", "xml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "xml")
}

func TestNewLogger_InvalidLevel(t *testing.T) {
	_, err := telemetry.NewLogger(&bytes.Buffer{}, "verbose", "json")
	require.Error(t, err)
}
