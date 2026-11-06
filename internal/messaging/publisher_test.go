package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryDelay_ExponentialBackoff(t *testing.T) {
	cases := []struct {
		retryCount int
		wantOK     bool
		wantDelay  string
	}{
		{0, true, "1s"},
		{1, true, "2s"},
		{2, true, "4s"},
		{3, true, "8s"},
		{4, true, "16s"},
		{5, false, ""},
		{-1, false, ""},
	}
	for _, c := range cases {
		delay, ok := RetryDelay(c.retryCount)
		require.Equal(t, c.wantOK, ok, "retryCount=%d", c.retryCount)
		if ok {
			require.Equal(t, c.wantDelay, delay.String(), "retryCount=%d", c.retryCount)
		}
	}
}

func TestNewMessageID_IsUnique(t *testing.T) {
	require.NotEqual(t, newMessageID(), newMessageID())
}

// Подключение к недоступному брокеру обязано падать быстро: health-проба
// ходит в Ping регулярно, и зависший dial заблокировал бы публикацию событий.
func TestNewRabbitPublisher_UnreachableBroker(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := NewRabbitPublisher("amqp://127.0.0.1:1/")
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(dialTimeout + 5*time.Second):
		t.Fatal("подключение к недоступному брокеру не завершилось за отведённое время")
	}
}

// После Close издатель не открывает соединение заново: иначе проба /health,
// пришедшая в момент остановки пода, воскресила бы соединение и задержала выход.
func TestRabbitPublisher_ClosedStaysClosed(t *testing.T) {
	p := &RabbitPublisher{url: "amqp://127.0.0.1:1/", closed: true}

	require.Error(t, p.Ping(context.Background()))

	// Close идемпотентен и на уже закрытом издателе не падает.
	require.NoError(t, p.Close())
}
