package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// unset удаляет переменные окружения, чтобы проверить дефолтные значения.
func unset(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		require.NoError(t, os.Unsetenv(k))
	}
}

// setRequired заполняет обязательные переменные тестовыми значениями.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	t.Setenv("S3_ACCESS_KEY", "test-access-key")
	t.Setenv("S3_SECRET_KEY", "test-secret-key")
}

func TestLoad_MissingRequiredFails(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "RABBITMQ_URL", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		t.Setenv(key, "")
	}

	cfg, err := Load()

	require.Nil(t, cfg)
	require.Error(t, err)
	// Понятное сообщение перечисляет все недостающие переменные.
	for _, key := range []string{"DATABASE_URL", "RABBITMQ_URL", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		require.Contains(t, err.Error(), key)
	}
}

func TestLoad_PartialMissingListsOnlyMissing(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RABBITMQ_URL", "")
	t.Setenv("S3_ACCESS_KEY", "ak")
	t.Setenv("S3_SECRET_KEY", "sk")

	_, err := Load()

	require.Error(t, err)
	require.Contains(t, err.Error(), "DATABASE_URL")
	require.Contains(t, err.Error(), "RABBITMQ_URL")
	require.NotContains(t, err.Error(), "S3_ACCESS_KEY")
	require.NotContains(t, err.Error(), "S3_SECRET_KEY")
}

func TestLoad_SuccessWithDefaults(t *testing.T) {
	setRequired(t)
	unset(t, "HTTP_ADDR", "STATIC_DIR", "S3_ENDPOINT", "S3_BUCKET", "S3_REGION", "S3_USE_SSL")

	cfg, err := Load()
	require.NoError(t, err)

	// Безопасные дефолты для несекретных значений.
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, "./web/static", cfg.StaticDir)
	require.Equal(t, "localhost:9000", cfg.Storage.Endpoint)
	require.Equal(t, "avatars", cfg.Storage.Bucket)
	require.Equal(t, "us-east-1", cfg.Storage.Region)
	require.False(t, cfg.Storage.UseSSL)
}

func TestLoad_ExplicitEmptyKeptEmpty(t *testing.T) {
	setRequired(t)
	// Переменная объявлена, но пустая: это явное значение, а не «не задана» —
	// дефолт подставляться не должен.
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("STATIC_DIR", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "", cfg.HTTPAddr)
	require.Equal(t, "", cfg.StaticDir)
}

func TestLoad_ObservabilityDefaults(t *testing.T) {
	setRequired(t)
	unset(t, "LOG_LEVEL", "LOG_FORMAT", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"METRICS_ADDR", "RABBITMQ_MANAGEMENT_URL",
		"RABBITMQ_MANAGEMENT_USER", "RABBITMQ_MANAGEMENT_PASSWORD")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "info", cfg.LogLevel)
	require.Equal(t, "json", cfg.LogFormat)
	require.Equal(t, "", cfg.OTLPEndpoint, "без коллектора трейсинг выключен")
	require.Equal(t, ":9091", cfg.MetricsAddr)
	require.Equal(t, "", cfg.RabbitMQManagementURL, "сбор метрик очередей выключен")
}

func TestLoad_ObservabilityFromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://jaeger:4318")
	t.Setenv("METRICS_ADDR", ":9999")
	t.Setenv("RABBITMQ_MANAGEMENT_URL", "http://rabbitmq:15672")
	t.Setenv("RABBITMQ_MANAGEMENT_USER", "admin")
	t.Setenv("RABBITMQ_MANAGEMENT_PASSWORD", "secret")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "debug", cfg.LogLevel)
	require.Equal(t, "text", cfg.LogFormat)
	require.Equal(t, "http://jaeger:4318", cfg.OTLPEndpoint)
	require.Equal(t, ":9999", cfg.MetricsAddr)
	require.Equal(t, "http://rabbitmq:15672", cfg.RabbitMQManagementURL)
	require.Equal(t, "admin", cfg.RabbitMQManagementUser)
	require.Equal(t, "secret", cfg.RabbitMQManagementPassword)
}

func TestLoad_ExplicitEmptyOTLPEndpoint(t *testing.T) {
	setRequired(t)
	// Явно выключенный трейсинг не должен подменяться дефолтом.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "", cfg.OTLPEndpoint)
}

func TestLoad_FromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("S3_ENDPOINT", "minio:9000")
	t.Setenv("S3_BUCKET", "photos")
	t.Setenv("S3_REGION", "eu-west-1")
	t.Setenv("S3_USE_SSL", "true")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, ":9090", cfg.HTTPAddr)
	require.Equal(t, "postgres://u:p@h/db", cfg.DatabaseURL)
	require.Equal(t, "amqp://guest:guest@localhost:5672/", cfg.RabbitMQURL)
	require.Equal(t, "minio:9000", cfg.Storage.Endpoint)
	require.Equal(t, "test-access-key", cfg.Storage.AccessKey)
	require.Equal(t, "test-secret-key", cfg.Storage.SecretKey)
	require.Equal(t, "photos", cfg.Storage.Bucket)
	require.Equal(t, "eu-west-1", cfg.Storage.Region)
	require.True(t, cfg.Storage.UseSSL)
}
