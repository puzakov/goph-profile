# Основные команды разработки GophProfile.

.PHONY: build test test-cover lint test-integration compose-up compose-down compose-clean \
	compose-up-monitoring compose-down-monitoring \
	docker-build-minikube k8s-apply k8s-delete k8s-apply-monitoring namespace-psa \
	helm-lint helm-template helm-install helm-uninstall

# Сборка всех бинарников (server, worker, migrate).
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

# --- Kubernetes ---------------------------------------------------------
# Переменные можно переопределить: make helm-install RELEASE=my-profile.

NAMESPACE ?= goph-profile
RELEASE   ?= goph-profile
CHART     ?= deploy/helm/goph-profile
IMAGE     ?= goph-profile:latest
VALUES    ?=

# Сборка образа внутри minikube: образ не покидает кластер, поэтому
# imagePullPolicy: IfNotPresent и тег latest не требуют реестра.
#
# Именно minikube image build, а не `eval $(minikube docker-env) && docker
# build`: при container-runtime=containerd (значение по умолчанию) образы
# собираются в containerd, а docker-env указывает на отдельный демон и
# kubelet такой образ не увидит.
docker-build-minikube:
	minikube image build -t $(IMAGE) -f docker/Dockerfile .

# Установка плоскими манифестами (альтернатива чарту; не совмещать с helm-install).
k8s-apply:
	kubectl apply -f k8s/

# Удаление ресурсов плоского пути.
k8s-delete:
	kubectl delete -f k8s/ --ignore-not-found

# ServiceMonitor и PrometheusRule: применять после установки Prometheus Operator.
k8s-apply-monitoring:
	kubectl apply -f k8s/monitoring/

# Pod Security Admission для Helm-пути: namespace создаёт сам Helm
# (--create-namespace), поэтому политика ставится лейблами отдельно.
# Заменяет PodSecurityPolicy, удалённый из Kubernetes в 1.25.
# В плоском пути те же лейблы уже стоят в k8s/00-namespace.yaml.
namespace-psa:
	kubectl label namespace $(NAMESPACE) --overwrite \
		pod-security.kubernetes.io/enforce=restricted \
		pod-security.kubernetes.io/enforce-version=latest

# Проверка чарта без кластера.
helm-lint:
	helm lint $(CHART)

# Рендер манифестов: посмотреть результат до установки.
helm-template:
	helm template $(RELEASE) $(CHART) -n $(NAMESPACE)

# Установка чарта (каноничный способ). Секреты в проде передаются так:
#   make helm-install VALUES=deploy/helm/goph-profile/values-prod.yaml
helm-install:
	helm upgrade --install $(RELEASE) $(CHART) \
		-n $(NAMESPACE) --create-namespace \
		$(if $(VALUES),-f $(VALUES),)

# Удаление релиза. PVC сохраняются: данные переживают переустановку.
helm-uninstall:
	helm uninstall $(RELEASE) -n $(NAMESPACE)
