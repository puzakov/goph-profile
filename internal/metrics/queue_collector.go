package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// queueRequestTimeout — предел на запрос к RabbitMQ Management API.
const queueRequestTimeout = 5 * time.Second

// queuePrefix — очереди сервиса, по которым собирается статистика.
const queuePrefix = "avatar."

// rabbitQueue — часть ответа /api/queues, нужная для метрик.
type rabbitQueue struct {
	Name      string `json:"name"`
	Messages  int    `json:"messages"`
	Consumers int    `json:"consumers"`
}

// queueCollector опрашивает RabbitMQ Management API при каждом scrape:
// глубина очередей — это состояние брокера, своей копии сервис не держит.
type queueCollector struct {
	client   *http.Client
	url      string
	user     string
	password string
	log      *slog.Logger

	messagesDesc  *prometheus.Desc
	consumersDesc *prometheus.Desc
}

// NewQueueCollector возвращает коллектор метрик очередей RabbitMQ.
// baseURL — адрес Management API, например http://rabbitmq:15672.
func NewQueueCollector(baseURL, user, password string, log *slog.Logger) prometheus.Collector {
	return &queueCollector{
		client:   &http.Client{Timeout: queueRequestTimeout},
		url:      strings.TrimSuffix(baseURL, "/") + "/api/queues/%2F",
		user:     user,
		password: password,
		log:      log,
		messagesDesc: prometheus.NewDesc(
			namespace+"_queue_messages",
			"Число готовых к доставке сообщений в очереди.",
			[]string{"queue"}, nil),
		consumersDesc: prometheus.NewDesc(
			namespace+"_queue_consumers",
			"Число консьюмеров очереди.",
			[]string{"queue"}, nil),
	}
}

// Describe отправляет описания метрик коллектора.
func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.messagesDesc
	ch <- c.consumersDesc
}

// Collect опрашивает Management API и отдаёт метрики по очередям сервиса.
// Ошибка опроса не ломает scrape: она логируется, метрик просто нет.
func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	queues, err := c.fetchQueues()
	if err != nil {
		c.log.Error("collect rabbitmq queues", "error", err)
		return
	}
	for _, q := range queues {
		if !strings.HasPrefix(q.Name, queuePrefix) {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.messagesDesc, prometheus.GaugeValue,
			float64(q.Messages), q.Name)
		ch <- prometheus.MustNewConstMetric(c.consumersDesc, prometheus.GaugeValue,
			float64(q.Consumers), q.Name)
	}
}

// fetchQueues запрашивает список очередей брокера.
func (c *queueCollector) fetchQueues() ([]rabbitQueue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queueRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.SetBasicAuth(c.user, c.password)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get queues: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get queues: unexpected status %s", resp.Status)
	}
	var queues []rabbitQueue
	if err := json.NewDecoder(resp.Body).Decode(&queues); err != nil {
		return nil, fmt.Errorf("decode queues: %w", err)
	}
	return queues, nil
}
