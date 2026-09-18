package handlers

import (
	"log/slog"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRouter собирает HTTP-роутер: REST API, healthcheck и веб-интерфейс.
func NewRouter(svc AvatarService, staticDir string, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Tracing снаружи RequestLogger: логгер и хендлеры должны видеть спан
	// в контексте запроса, чтобы писать trace_id.
	r.Use(Tracing())
	r.Use(RequestLogger(log))
	r.Use(middleware.Recoverer)

	api := NewAvatarHandler(svc, log)

	// Healthcheck.
	r.Get("/health", api.Health)

	// Метрики: регистрируются до статики, иначе FileServer перехватит путь.
	r.Handle("/metrics", promhttp.Handler())

	// REST API.
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/avatars", api.Upload)                       // загрузка
		r.Get("/avatars/{avatarID}", api.GetAvatar)          // получение файла
		r.Get("/avatars/{avatarID}/metadata", api.Metadata)  // метаданные
		r.Delete("/avatars/{avatarID}", api.DeleteAvatar)    // удаление
		r.Get("/users/{userID}/avatar", api.GetByUserAvatar) // аватарка пользователя
		r.Delete("/users/{userID}/avatar", api.DeleteByUser) // удаление аватарки пользователя
		r.Get("/users/{userID}/avatars", api.ListByUser)     // список аватарок
	})

	// Веб-интерфейс: форма загрузки.
	r.Get("/web/upload", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFile(w, req, filepath.Join(staticDir, "index.html"))
	})
	// Остальная статика (главная страница и т.д.).
	r.Handle("/*", http.FileServer(http.Dir(staticDir)))

	return r
}
