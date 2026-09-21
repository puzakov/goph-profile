# GophProfile — микросервис аватарок

Пользователь загружает фотографию один раз, а сторонние платформы (блоги, форумы,
сервисы комментариев) запрашивают аватарку по идентификатору пользователя.
Если аватарки нет — отдаётся заглушка (появится на следующих этапах).

## Стек

- **Go 1.27**, HTTP-роутер **chi**
- **PostgreSQL 16** — метаданные аватарок; миграции через **goose** (up/down, встроены в бинарник)
- **MinIO** (S3-совместимо) — файлы изображений
- **RabbitMQ** — события обработки, ретраи с экспоненциальным backoff через DLX
- **OpenTelemetry** — трейсинг (HTTP, PostgreSQL, S3, RabbitMQ), метрики, корреляция логов
- **Prometheus + Grafana + Loki + Jaeger + Alertmanager** — мониторинг локального стенда
- **Docker Compose** — локальный стенд
- Тесты: unit (pgxmock, testify), интеграционные (testcontainers-go, testify/suite), `golangci-lint`

## Архитектура

```
                        ┌─────────────┐   AvatarUploadEvent   ┌────────────┐
POST /api/v1/avatars ─▶ │    server   │ ─────────────────────▶ │  RabbitMQ  │
  (multipart)           └──────┬──────┘   AvatarDeleteEvent   └─────┬──────┘
                              │  файл                    AvatarProcessEvent│
                              ▼                                     ┌─────▼─────┐
                        ┌──────────┐                                │   worker  │
                        │   MinIO  │  ◀── скачать оригинал ──────── │  ресайз   │
                        │   (S3)   │  ◀── загрузить миниатюры ───── │ 100/300px │
                        └──────────┘  ◀── удалить файлы ────────────└───────────┘
                        ┌──────────┐
                        │PostgreSQL│  метаданные, статусы, event_dedup
                        └──────────┘
```

**Конвейер обработки** (все события идемпотентны по `message_id` + статусу):

1. `POST` сохраняет оригинал в S3 и метаданные в БД, статус `pending`, публикует `AvatarUploadEvent`.
2. Воркер по `avatar.uploaded` переводит статус в `processing` и публикует `AvatarProcessEvent` с операциями `resize_100x100`, `resize_300x300`.
3. Воркер по `avatar.process` создаёт миниатюры **JPEG** (квадратный центр-кроп, белый фон для прозрачности) в `thumbnails/{id}/{size}.jpg` и ставит `ready`.
4. `DELETE` мягко удаляет строку и публикует `AvatarDeleteEvent` — файлы из S3 убирает воркер.

**Ретраи**: при retryable-ошибке сообщение уходит в retry-очередь с TTL по схеме
1с → 2с → 4с → 8с → 16с; после 5 попыток — в dead-letter очередь, аватарка помечается `failed`.

## REST API

| Метод | Путь | Описание |
|---|---|---|
| POST | `/api/v1/avatars` | Загрузка аватарки (multipart `image`/`file`, заголовок `X-User-ID`) → `201` |
| GET | `/api/v1/avatars/{id}` | Файл аватарки; `?size=original\|100x100\|300x300`, `?format=jpeg\|png\|webp` (для оригинала) |
| GET | `/api/v1/avatars/{id}/metadata` | Метаданные (размеры, миниатюры, статус) |
| DELETE | `/api/v1/avatars/{id}` | Удаление (заголовок `X-User-ID`) → `204` / `403` чужой |
| GET | `/api/v1/users/{userID}/avatar` | Текущая аватарка пользователя |
| DELETE | `/api/v1/users/{userID}/avatar` | Удаление текущей аватарки |
| GET | `/api/v1/users/{userID}/avatars` | Список аватарок пользователя |
| GET | `/health` | Проверка БД, S3 и брокера → `200` / `503` |

Форматы: JPEG, PNG, WebP. Максимальный размер — 10 МБ.
Файлы отдаются с `Cache-Control: max-age=86400` и `ETag` (поддержка `If-None-Match` → `304`).

OpenAPI-спецификация: `internal/api/openapi.yaml`.

## Запуск

### Docker Compose (рекомендуется)

```bash
docker compose -f docker/docker-compose.yml up --build
```

Сервис: `http://localhost:8080` (веб-интерфейс на `/`), MinIO-консоль: `http://localhost:9001`,
RabbitMQ management: `http://localhost:15672` (guest/guest).

### Локально (без Docker)

```bash
go build ./...
HTTP_ADDR=:8080 DATABASE_URL=... S3_ENDPOINT=localhost:9000 RABBITMQ_URL=... ./server
RABBITMQ_URL=... ./worker
```

Настройки — в `.env.example`.

## Наблюдаемость

Три сигнала сшиты одним `trace_id`: HTTP-запрос на входе → спан сервиса →
SQL-запрос, вызов S3 и публикация в RabbitMQ → обработка в воркере.

### Трейсинг

Спаны уходят по OTLP/HTTP в Jaeger (`OTEL_EXPORTER_OTLP_ENDPOINT`, по умолчанию
`http://localhost:4318`). Пустое значение выключает экспорт — сервис работает
с no-op провайдером, так запускаются тесты.

Инструментированы HTTP-слой (`otelhttp`), PostgreSQL (`otelpgx`), S3 и публикация
событий. Контекст трейса переносится в заголовках HTTP и в headers AMQP-сообщения,
поэтому трейс воркера — продолжение трейса сервера.

```bash
curl -X POST http://localhost:8080/api/v1/avatars -H "X-User-ID: user-1" -F "image=@photo.png"
# Jaeger: http://localhost:16686 → service avatar-server → трейс POST /api/v1/avatars
```

### Метрики

`/metrics` отдаёт сервер (на `HTTP_ADDR`) и воркер (на `METRICS_ADDR`, по умолчанию `:9091`).

| Метрика | Тип | Описание |
|---|---|---|
| `avatars_uploads_total{status}` | counter | Загрузки аватарок: `ok` / `error` |
| `avatars_upload_duration_seconds{status}` | histogram | Длительность загрузки |
| `avatars_http_requests_total{method,route,status}` | counter | RED по HTTP-запросам |
| `avatars_http_request_duration_seconds{method,route,status}` | histogram | Длительность запросов |
| `avatars_storage_bytes` | gauge | Суммарный размер живых аватарок в S3 |
| `avatars_users_with_avatars` | gauge | Число пользователей с живыми аватарками |
| `avatars_queue_messages{queue}`, `avatars_queue_consumers{queue}` | gauge | Глубина очередей RabbitMQ |
| `avatars_db_*` | gauge/counter | Пул соединений PostgreSQL |

Метка `route` — шаблон маршрута (`/api/v1/avatars/{avatarID}`), а не путь запроса:
иначе кардинальность растёт с каждым идентификатором. Незаматченные пути
считаются как `unmatched`.

Метрики очередей собираются с RabbitMQ Management API и включаются только при
заданном `RABBITMQ_MANAGEMENT_URL`.

Каждый сервис отдаёт метрики своего процесса (`avatars_db_*` — свой пул
соединений, сервер — HTTP и загрузки). Метрики состояния системы
(`avatars_storage_bytes`, `avatars_users_with_avatars`, `avatars_queue_*`) отдаёт
только воркер: значение не зависит от процесса, а два источника одного gauge
дали бы двойной счёт в `sum()`.

Объём хранилища — агрегат без разбивки по пользователям: метка `user_id` росла
бы вместе с числом пользователей и в сумме с их аватарками (неограниченная
кардинальность). Запрос агрегата выполняется не на каждый scrape, а раз в
`StorageCacheTTL` (минута), поэтому при недоступной БД отдаётся последний
удачный снимок, а не пустая метрика.

### Логи

JSON в stdout: `time`, `level`, `msg`, `trace_id`, `span_id` плюс поля запроса
(`request_id`, `method`, `path`, `route`, `status`, `bytes`, `duration_ms`).
Уровень и формат — `LOG_LEVEL` (`debug|info|warn|error`) и `LOG_FORMAT` (`json|text`).

```bash
docker compose -f docker/docker-compose.yml logs server | jq 'select(.msg=="request")'
```

### Стенд мониторинга

```bash
cp docker/.env.example docker/.env    # задать GRAFANA_PASSWORD (дефолтной admin/admin нет)
make compose-up                       # основной стенд
make compose-up-monitoring            # Prometheus, Grafana, Loki, Jaeger, Alertmanager
```

| Сервис | Адрес | Что смотреть |
|---|---|---|
| Grafana | http://localhost:3000 | Дашборды `GophProfile`: Service overview, Business KPIs, Infrastructure |
| Prometheus | http://localhost:9090 | `/targets`, `/alerts` |
| Jaeger | http://localhost:16686 | Трейсы сквозь server и worker |
| Loki | http://localhost:3100 | Логи (через Grafana → Explore; из лога можно перейти в трейс) |
| Alertmanager | http://localhost:9093 | Сработавшие алерты |

Логи собирает Promtail через docker-сокет, поэтому основной стенд не меняется.
Алерты описаны в `docker/monitoring/prometheus/rules.yml`:
`HighErrorRate` (доля ошибок загрузок > 10% в течение 5 минут, warning) и
`HighResponseTime` (p95 загрузки > 5 с в течение 2 минут, critical).

## Разработка

```bash
make build             # сборка
make test              # unit-тесты
make test-cover        # тесты + покрытие (требование: >50%)
make test-integration  # интеграционные тесты (testcontainers, нужен Docker)
make lint              # golangci-lint
make compose-up        # локальный стенд
make compose-up-monitoring    # стенд мониторинга (запускать вторым)
make compose-down-monitoring  # остановить мониторинг
make migrate-up        # применить миграции через goose CLI (вне сервиса)
make migrate-down      # откатить последнюю миграцию
make migrate-status    # статус миграций
```

Миграции применяются автоматически при старте server и worker. Для ручного
управления используйте goose CLI (`make migrate-up/down/status`, DSN из `DATABASE_URL`).

## Структура проекта

```
├── cmd/
│   ├── server/        # HTTP-сервер (REST API + веб-интерфейс)
│   └── worker/        # воркер фоновой обработки изображений
├── internal/
│   ├── api/           # OpenAPI-спецификация
│   ├── config/        # конфигурация из переменных окружения
│   ├── db/
│   │   └── migrations/ # SQL-миграции goose (go:embed в бинарник)
│   ├── domain/        # сущности, статусы, события брокера
│   ├── handlers/      # HTTP-обработчики, роутер, middleware
│   ├── imaging/       # утилиты работы с изображениями (crop/resize/JPEG)
│   ├── messaging/     # RabbitMQ: publisher, топология, retry-политика, propagation трейса
│   ├── metrics/       # метрики Prometheus и коллекторы (пул БД, S3, очереди)
│   ├── repository/    # PostgreSQL: метаданные, event_dedup
│   ├── services/      # бизнес-логика
│   ├── storage/       # S3-совместимое хранилище (MinIO)
│   ├── telemetry/     # OpenTelemetry: трейсинг, логирование, корреляция
│   └── worker/        # консьюмеры событий, конвейер обработки
├── docker/            # Dockerfile, docker-compose, конфиги мониторинга
├── tests/integration/ # интеграционные тесты (testcontainers-go)
└── web/static/        # фронтенд (форма загрузки)
```

## Статусы обработки

`pending` → `processing` → `ready`; при фатальных ошибках или исчерпании ретраев — `failed`.
