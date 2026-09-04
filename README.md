# GophProfile — микросервис аватарок

Пользователь загружает фотографию один раз, а сторонние платформы (блоги, форумы,
сервисы комментариев) запрашивают аватарку по идентификатору пользователя.
Если аватарки нет — отдаётся заглушка (появится на следующих этапах).

## Стек

- **Go 1.27**, HTTP-роутер **chi**
- **PostgreSQL 16** — метаданные аватарок; миграции через **goose** (up/down, встроены в бинарник)
- **MinIO** (S3-совместимо) — файлы изображений
- **RabbitMQ** — события обработки, ретраи с экспоненциальным backoff через DLX
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

## Разработка

```bash
make build             # сборка
make test              # unit-тесты
make test-cover        # тесты + покрытие (требование: >50%)
make test-integration  # интеграционные тесты (testcontainers, нужен Docker)
make lint              # golangci-lint
make compose-up        # локальный стенд
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
│   ├── messaging/     # RabbitMQ: publisher, топология, retry-политика
│   ├── repository/    # PostgreSQL: метаданные, event_dedup
│   ├── services/      # бизнес-логика
│   ├── storage/       # S3-совместимое хранилище (MinIO)
│   └── worker/        # консьюмеры событий, конвейер обработки
├── docker/            # Dockerfile, docker-compose
├── tests/integration/ # интеграционные тесты (testcontainers-go)
└── web/static/        # фронтенд (форма загрузки)
```

## Статусы обработки

`pending` → `processing` → `ready`; при фатальных ошибках или исчерпании ретраев — `failed`.
