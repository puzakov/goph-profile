package handlers

import (
	"net/http"
)

// Health обрабатывает GET /health — проверку работоспособности компонентов.
func (h *AvatarHandler) Health(w http.ResponseWriter, r *http.Request) {
	components := h.svc.Health(r.Context())

	status, code := "ok", http.StatusOK
	for _, state := range components {
		if state != "ok" {
			status, code = "degraded", http.StatusServiceUnavailable
			break
		}
	}
	h.writeJSON(w, code, map[string]any{"status": status, "components": components})
}
