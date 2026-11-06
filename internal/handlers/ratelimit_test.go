package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testHandler — заглушка, отвечающая 204 и считающая вызовы.
type testHandler struct{ calls int }

func (h *testHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls++
	w.WriteHeader(http.StatusNoContent)
}

// newTestRequest создаёт запрос с клиентом user-X к заданному пути.
func newTestRequest(user string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/avatars", nil)
	r.Header.Set("X-User-ID", user)
	return r
}

// call выполняет запрос через лимитер и возвращает код ответа.
func call(t *testing.T, rl *RateLimiter, next http.Handler, user string) (int, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	rl.Middleware(next).ServeHTTP(rec, newTestRequest(user))
	return rec.Code, rec.Header()
}

func TestRateLimit_AllowsWithinBurst(t *testing.T) {
	rl := NewRateLimiter(1, 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	for i := 0; i < 3; i++ {
		code, _ := call(t, rl, next, "user-1")
		require.Equal(t, http.StatusNoContent, code, "запрос %d в пределах burst", i+1)
	}
	require.Equal(t, 3, next.calls)
}

func TestRateLimit_BlocksAfterBurst(t *testing.T) {
	rl := NewRateLimiter(1, 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	code, _ := call(t, rl, next, "user-1")
	require.Equal(t, http.StatusNoContent, code)
	code, _ = call(t, rl, next, "user-1")
	require.Equal(t, http.StatusNoContent, code)

	code, hdr := call(t, rl, next, "user-1")
	require.Equal(t, http.StatusTooManyRequests, code)
	require.NotEmpty(t, hdr.Get("Retry-After"), "заголовок Retry-After обязателен")
	// Дальше клиент не проходит к хендлеру.
	require.Equal(t, 2, next.calls)
}

func TestRateLimit_ClientsAreIndependent(t *testing.T) {
	rl := NewRateLimiter(1, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	code, _ := call(t, rl, next, "user-1")
	require.Equal(t, http.StatusNoContent, code)
	// Тот же лимит, но другой клиент: его ведро полно.
	code, _ = call(t, rl, next, "user-2")
	require.Equal(t, http.StatusNoContent, code)
	code, _ = call(t, rl, next, "user-1")
	require.Equal(t, http.StatusTooManyRequests, code)
}

func TestRateLimit_TokensRefill(t *testing.T) {
	rl := NewRateLimiter(2, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	code, _ := call(t, rl, next, "user-1")
	require.Equal(t, http.StatusNoContent, code)
	code, _ = call(t, rl, next, "user-1")
	require.Equal(t, http.StatusTooManyRequests, code)

	// Со скоростью 2 токена/с одного токена хватает на полсекунды.
	time.Sleep(700 * time.Millisecond)
	code, _ = call(t, rl, next, "user-1")
	require.Equal(t, http.StatusNoContent, code)
}

func TestRateLimit_DisabledPassesEverything(t *testing.T) {
	rl := NewRateLimiter(0, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	for i := 0; i < 100; i++ {
		code, _ := call(t, rl, next, "user-1")
		require.Equal(t, http.StatusNoContent, code, "запрос %d", i+1)
	}
	require.Equal(t, 100, next.calls)
}

func TestRateLimit_ClientKeyPrefersUserID(t *testing.T) {
	require.Equal(t, "user-7", clientKey(newTestRequest("user-7")))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	require.Equal(t, "10.0.0.5", clientKey(r))
}

// TestRateLimit_RetryAfterIsPositive проверяет, что Retry-After — целое
// положительное число секунд (пригодно для клиентских таймеров).
func TestRateLimit_RetryAfterIsPositive(t *testing.T) {
	rl := NewRateLimiter(1, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	next := &testHandler{}

	call(t, rl, next, "user-1")
	_, hdr := call(t, rl, next, "user-1")

	sec, err := strconv.Atoi(hdr.Get("Retry-After"))
	require.NoError(t, err)
	require.Positive(t, sec)
}
