// Package config загружает настройки сервиса из переменных окружения.
package config

import (
	"fmt"
	"os"
	"strings"
)

// requiredEnv — переменные окружения, без которых сервис не стартует.
var requiredEnv = []string{
	"DATABASE_URL",
	"RABBITMQ_URL",
	"S3_ACCESS_KEY",
	"S3_SECRET_KEY",
}

// Config — настройки сервиса.
type Config struct {
	HTTPAddr    string // адрес HTTP-сервера, например ":8080"
	DatabaseURL string // DSN подключения к PostgreSQL
	StaticDir   string // каталог со статическими файлами веб-интерфейса
	RabbitMQURL string // адрес брокера сообщений RabbitMQ
	Storage     StorageConfig

	// Наблюдаемость.
	LogLevel     string // уровень логирования: debug, info, warn, error
	LogFormat    string // формат логов: json или text
	OTLPEndpoint string // URL OTLP/HTTP-коллектора; пусто — трейсинг выключен
	MetricsAddr  string // адрес /metrics воркера (сервер отдаёт метрики на HTTPAddr)

	// RabbitMQ Management API — источник метрик очередей.
	RabbitMQManagementURL      string
	RabbitMQManagementUser     string
	RabbitMQManagementPassword string
}

// StorageConfig — настройки S3-совместимого хранилища.
type StorageConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// Load читает конфигурацию из окружения
func Load() (*Config, error) {
	if missing := missingRequired(); len(missing) > 0 {
		return nil, fmt.Errorf("required environment variables are not set: %s",
			strings.Join(missing, ", "))
	}
	return &Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("DATABASE_URL", ""),
		StaticDir:   getenv("STATIC_DIR", "./web/static"),
		RabbitMQURL: getenv("RABBITMQ_URL", ""),
		Storage: StorageConfig{
			Endpoint:  getenv("S3_ENDPOINT", "localhost:9000"),
			AccessKey: getenv("S3_ACCESS_KEY", ""),
			SecretKey: getenv("S3_SECRET_KEY", ""),
			Bucket:    getenv("S3_BUCKET", "avatars"),
			Region:    getenv("S3_REGION", "us-east-1"),
			UseSSL:    getenvBool("S3_USE_SSL", false),
		},
		LogLevel:     getenv("LOG_LEVEL", "info"),
		LogFormat:    getenv("LOG_FORMAT", "json"),
		OTLPEndpoint: getenv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		MetricsAddr:  getenv("METRICS_ADDR", ":9091"),
		// Пустой RABBITMQ_MANAGEMENT_URL выключает сбор метрик очередей.
		RabbitMQManagementURL:      getenv("RABBITMQ_MANAGEMENT_URL", ""),
		RabbitMQManagementUser:     getenv("RABBITMQ_MANAGEMENT_USER", "guest"),
		RabbitMQManagementPassword: getenv("RABBITMQ_MANAGEMENT_PASSWORD", "guest"),
	}, nil
}

// missingRequired возвращает имена обязательных переменных с пустым значением.
func missingRequired() []string {
	var missing []string
	for _, key := range requiredEnv {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	return v == "true" || v == "1"
}
