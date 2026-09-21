# Основные команды разработки GophProfile.

.PHONY: build test test-cover lint test-integration compose-up compose-down compose-clean \
	compose-up-monitoring compose-down-monitoring

# Сборка всех бинарников (server, worker).
build:
	go build ./...

# Unit-тесты всех пакетов.
test:
	go test ./...

# Unit-тесты с отчётом о покрытии (требование: >50%).
test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Статический анализ golangci-lint.
lint:
	golangci-lint run ./...

# Интеграционные тесты: testcontainers-go поднимает PostgreSQL,
# RabbitMQ и MinIO прямо из тестов (требуется Docker).
test-integration:
	go test -tags=integration -v -timeout 15m ./tests/integration/

# Применение миграций через goose CLI (вне сервиса).
# Требуется запущенная БД; DSN берётся из DATABASE_URL или .env.
migrate-up:
	go run github.com/pressly/goose/v3/cmd/goose@latest \
		-dir internal/db/migrations postgres "$(DATABASE_URL)" up

# Откат последней миграции.
migrate-down:
	go run github.com/pressly/goose/v3/cmd/goose@latest \
		-dir internal/db/migrations postgres "$(DATABASE_URL)" down

# Статус применённых миграций.
migrate-status:
	go run github.com/pressly/goose/v3/cmd/goose@latest \
		-dir internal/db/migrations postgres "$(DATABASE_URL)" status

# Локальный стенд: PostgreSQL + MinIO + RabbitMQ + server + worker.
compose-up:
	docker compose -f docker/docker-compose.yml up -d --build

# Остановить стенд без удаления данных.
compose-down:
	docker compose -f docker/docker-compose.yml down

# Остановить стенд и удалить данные (volumes).
compose-clean:
	docker compose -f docker/docker-compose.yml down -v

# Мониторинг: Prometheus + Jaeger + Grafana + Loki + Alertmanager.
# Запускать вторым: сеть goph_default создаёт основной compose.
# Перед первым запуском: cp docker/.env.example docker/.env и задать
# GRAFANA_PASSWORD — дефолтной пары admin/admin у стенда нет.
compose-up-monitoring:
	docker compose -f docker/docker-compose.monitoring.yml up -d

# Остановить мониторинг (данные в volumes сохраняются).
compose-down-monitoring:
	docker compose -f docker/docker-compose.monitoring.yml down
