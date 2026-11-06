package handlers

import (
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// clientLimiter — состояние token bucket одного клиента.
type clientLimiter struct {
	tokens   float64 // доступные токены
	lastSeen time.Time
}

// RateLimiter ограничивает частоту запросов по клиенту. Ключ клиента —
// заголовок X-User-ID (его проставляет вызывающий), при отсутствии — IP
// источника. Токены восстанавливаются со скоростью rps в секунду,
// одномоментный всплеск ограничен burst токенами. При исчерпании клиент
// получает 429 с заголовком Retry-After.
//
// Реализация без внешних зависимостей: фиксированный пакет из
// golang.org/x/time умеет то же самое, но для одного простого правила
// проще держать сорок строк своего кода, чем новую зависимость.
type RateLimiter struct {
	mu        sync.Mutex
	clients   map[string]*clientLimiter
	rps       float64 // пополнение, токенов в секунду
	burst     float64 // ёмкость ведра
	lastSweep time.Time
	log       *slog.Logger
}

// sweepEvery — период чистки карты клиентов; idleAfter — сколько времени
// клиент без запросов считается ушедшим.
const (
	sweepEvery = time.Minute
	idleAfter  = 5 * time.Minute
)

// NewRateLimiter создаёт ограничитель. rps <= 0 отключает ограничение:
// Middleware становится прозрачным.
func NewRateLimiter(rps, burst int, log *slog.Logger) *RateLimiter {
	if burst <= 0 {
		burst = rps
	}
	return &RateLimiter{
		clients: make(map[string]*clientLimiter),
		rps:     float64(rps),
		burst:   float64(burst),
		log:     log,
	}
}

// Middleware возвращает http-обёртку для группы маршрутов REST API.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Отключённый лимитер: трафик идёт без ограничений.
		if rl.rps <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		if retryAfter := rl.consume(clientKey(r), time.Now()); retryAfter > 0 {
			// По спецификации Retry-After — целое число секунд.
			secs := int(math.Ceil(retryAfter.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			if err := json.NewEncoder(w).Encode(map[string]string{
				"error": "rate limit exceeded",
			}); err != nil {
				rl.log.Error("encode rate limit response", "error", err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey определяет клиента запроса: X-User-ID обязателен для API,
// IP — запасной вариант для анонимных обращений.
func clientKey(r *http.Request) string {
	if id := r.Header.Get("X-User-ID"); id != "" {
		return id
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// consume списывает токен клиента и возвращает время до освобождения
// лимита; ноль — запрос разрешён.
func (rl *RateLimiter) consume(key string, now time.Time) time.Duration {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if now.Sub(rl.lastSweep) >= sweepEvery {
		for k, c := range rl.clients {
			if now.Sub(c.lastSeen) > idleAfter {
				delete(rl.clients, k)
			}
		}
		rl.lastSweep = now
	}

	c, ok := rl.clients[key]
	if !ok {
		c = &clientLimiter{tokens: rl.burst}
		rl.clients[key] = c
	}
	// Пополнение за время, прошедшее с прошлого запроса клиента.
	elapsed := now.Sub(c.lastSeen).Seconds()
	c.lastSeen = now
	if c.tokens < rl.burst {
		c.tokens += elapsed * rl.rps
	}
	if c.tokens > rl.burst {
		c.tokens = rl.burst
	}
	if c.tokens >= 1 {
		c.tokens--
		return 0
	}
	// Секунды до накопления одного токена; вызывающий округлит вверх.
	return time.Duration((1 - c.tokens) / rl.rps * float64(time.Second))
}
