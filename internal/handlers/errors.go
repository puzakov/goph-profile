package handlers

import (
	"encoding/json"
	"net/http"
)

// writeJSON сериализует ответ в JSON и устанавливает Content-Type.
func (h *AvatarHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Error("encode response", "error", err)
	}
}

// writeError формирует ответ об ошибке в формате {"error": ..., "details": ...}.
func (h *AvatarHandler) writeError(w http.ResponseWriter, status int, message string, details any) {
	body := map[string]any{"error": message}
	if details != nil {
		body["details"] = details
	}
	h.writeJSON(w, status, body)
}

// writeInternalError логирует непредвиденную ошибку и отвечает 500.
func (h *AvatarHandler) writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("internal error",
		"method", r.Method, "path", r.URL.Path, "error", err)
	h.writeError(w, http.StatusInternalServerError, "Internal server error", nil)
}
