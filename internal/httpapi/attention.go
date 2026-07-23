package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) rnAttention(w http.ResponseWriter, r *http.Request) {
	limit := 40
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be an integer from 1 to %d", 500))
			return
		}
		limit = parsed
	}
	writeJSON(w, http.StatusOK, s.store.RNAttention(time.Now().UTC(), limit))
}
