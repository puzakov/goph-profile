// Package config загружает настройки сервиса из переменных окружения.
package config

import "os"

// Config — настройки сервиса.
type Config struct {
	HTTPAddr    string // адрес HTTP-сервера, например ":8080"
	DatabaseURL string // DSN подключения к PostgreSQL
	StaticDir   string // каталог со статическими файлами веб-интерфейса
	RabbitMQURL string // адрес брокера сообщений RabbitMQ
	Storage     StorageConfig
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

// Load читает конфигурацию из окружения, подставляя значения по умолчанию.
func Load() *Config {
	return &Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://goph:goph@localhost:5432/goph?sslmode=disable"),
		StaticDir:   getenv("STATIC_DIR", "./web/static"),
		RabbitMQURL: getenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		Storage: StorageConfig{
			Endpoint:  getenv("S3_ENDPOINT", "localhost:9000"),
			AccessKey: getenv("S3_ACCESS_KEY", "minioadmin"),
			SecretKey: getenv("S3_SECRET_KEY", "minioadmin"),
			Bucket:    getenv("S3_BUCKET", "avatars"),
			Region:    getenv("S3_REGION", "us-east-1"),
			UseSSL:    getenvBool("S3_USE_SSL", false),
		},
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1"
}
