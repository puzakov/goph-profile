# ---------- Стадия сборки ----------
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Сначала копируем модули — кэш слоёв работает, пока go.mod/go.sum не менялись.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server
RUN CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

# ---------- Финальный образ ----------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /out/ /usr/local/bin/
COPY web/ /app/web/

EXPOSE 8080

CMD ["server"]
