package metrics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"goph-profile/internal/metrics"
)

// newRabbitStub поднимает заглушку RabbitMQ Management API с заданным
// списком очередей и проверяет, что клиент пришёл с basic auth.
func newRabbitStub(t *testing.T, queues string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		require.True(t, ok, "запрос к Management API без учётных данных")
		require.Equal(t, "guest", user)
		require.Equal(t, "guest", password)
		require.Equal(t, "/api/queues/%2F", r.URL.EscapedPath())

		var body []map[string]any
		require.NoError(t, json.Unmarshal([]byte(queues), &body))
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQueueCollector_FiltersForeignQueues(t *testing.T) {
	srv := newRabbitStub(t, `[
		{"name": "avatar.uploaded.queue", "messages": 3, "consumers": 1},
		{"name": "avatar.deleted.queue", "messages": 0, "consumers": 1},
		{"name": "some.other.queue", "messages": 99, "consumers": 7}
	]`)

	values := gather(t, metrics.NewQueueCollector(srv.URL, "guest", "guest", testLogger()))

	require.Equal(t, float64(3), values["avatars_queue_messages_avatar.uploaded.queue"])
	require.Equal(t, float64(1), values["avatars_queue_consumers_avatar.uploaded.queue"])
	require.Equal(t, float64(0), values["avatars_queue_messages_avatar.deleted.queue"])
	require.NotContains(t, values, "avatars_queue_messages_some.other.queue",
		"очереди чужих сервисов не собираются")
}

func TestQueueCollector_BrokerUnavailableEmitsNothing(t *testing.T) {
	// Недоступный брокер не должен ломать scrape метрик сервиса.
	collector := metrics.NewQueueCollector("http://127.0.0.1:1", "guest", "guest", testLogger())

	values := gather(t, collector)

	require.Empty(t, values)
}
