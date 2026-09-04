package config

import "testing"

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("STATIC_DIR", "")
	t.Setenv("RABBITMQ_URL", "")
	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_ACCESS_KEY", "")
	t.Setenv("S3_SECRET_KEY", "")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_REGION", "")
	t.Setenv("S3_USE_SSL", "")

	cfg := Load()

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL == "" {
		t.Error("DatabaseURL should have a default")
	}
	if cfg.StaticDir != "./web/static" {
		t.Errorf("StaticDir = %q, want ./web/static", cfg.StaticDir)
	}
	if cfg.RabbitMQURL == "" {
		t.Error("RabbitMQURL should have a default")
	}
	if cfg.Storage.Bucket != "avatars" {
		t.Errorf("Bucket = %q, want avatars", cfg.Storage.Bucket)
	}
	if cfg.Storage.UseSSL {
		t.Error("UseSSL = true, want false by default")
	}
}

func TestLoad_FromEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("RABBITMQ_URL", "amqp://user:pass@rabbit:5672/vhost")
	t.Setenv("S3_ENDPOINT", "minio:9000")
	t.Setenv("S3_ACCESS_KEY", "ak")
	t.Setenv("S3_SECRET_KEY", "sk")
	t.Setenv("S3_BUCKET", "photos")
	t.Setenv("S3_REGION", "eu-west-1")
	t.Setenv("S3_USE_SSL", "true")

	cfg := Load()

	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want :9090", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://u:p@h/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.RabbitMQURL != "amqp://user:pass@rabbit:5672/vhost" {
		t.Errorf("RabbitMQURL = %q", cfg.RabbitMQURL)
	}
	if cfg.Storage.Endpoint != "minio:9000" {
		t.Errorf("Endpoint = %q, want minio:9000", cfg.Storage.Endpoint)
	}
	if cfg.Storage.AccessKey != "ak" || cfg.Storage.SecretKey != "sk" {
		t.Errorf("credentials = %q/%q, want ak/sk", cfg.Storage.AccessKey, cfg.Storage.SecretKey)
	}
	if cfg.Storage.Bucket != "photos" {
		t.Errorf("Bucket = %q, want photos", cfg.Storage.Bucket)
	}
	if cfg.Storage.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1", cfg.Storage.Region)
	}
	if !cfg.Storage.UseSSL {
		t.Error("UseSSL = false, want true")
	}
}
