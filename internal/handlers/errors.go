package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// writeJSON сериализует ответ в JSON и устанавливает Content-Type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

// writeError формирует ответ об ошибке в формате {"error": ..., "details": ...}.
func writeError(w http.ResponseWriter, status int, message string, details any) {
	body := map[string]any{"error": message}
	if details != nil {
		body["details"] = details
	}
	writeJSON(w, status, body)
}

// writeInternalError логирует непредвиденную ошибку и отвечает 500.
func writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("internal error",
		"method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "Internal server error", nil)
}
