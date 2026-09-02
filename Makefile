# Основные команды разработки GophProfile.

.PHONY: build test test-cover lint test-integration compose-up compose-down compose-clean

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

# Локальный стенд: PostgreSQL + MinIO + RabbitMQ + server + worker.
compose-up:
	docker compose -f docker/docker-compose.yml up -d --build

# Остановить стенд без удаления данных.
compose-down:
	docker compose -f docker/docker-compose.yml down

# Остановить стенд и удалить данные (volumes).
compose-clean:
	docker compose -f docker/docker-compose.yml down -v
