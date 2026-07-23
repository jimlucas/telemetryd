package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const maxPageLimit = 5_000

// pageSlice applies optional offset/limit query parameters while preserving the
// historical behavior of returning all items when limit is omitted.
func pageSlice[T any](items []T, r *http.Request) ([]T, int, int, error) {
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return nil, 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = parsed
	}

	limit := len(items)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageLimit {
			return nil, 0, 0, fmt.Errorf("limit must be an integer from 1 to %d", maxPageLimit)
		}
		limit = parsed
	}

	if offset >= len(items) {
		return []T{}, offset, limit, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end], offset, limit, nil
}

func containsFold(needle string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}
