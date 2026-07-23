package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telemetryd/internal/model"
	"telemetryd/internal/state"
)

func (s *Server) recent(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	query, err := recentQueryFromRequest(r, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.store.Recent(now, query))
}

func recentQueryFromRequest(r *http.Request, now time.Time) (state.RecentQuery, error) {
	values := r.URL.Query()
	result := state.RecentQuery{
		DeviceID:     strings.TrimSpace(values.Get("id")),
		BNID:         strings.TrimSpace(values.Get("bn")),
		PathContains: strings.TrimSpace(values.Get("path")),
		Search:       strings.TrimSpace(values.Get("q")),
	}

	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1_000 {
			return result, fmt.Errorf("limit must be an integer from 1 to 1000")
		}
		result.Limit = limit
	}

	if raw := strings.TrimSpace(values.Get("kind")); raw != "" {
		result.Kind = model.DeviceKind(strings.ToLower(raw))
		if result.Kind != model.KindBN && result.Kind != model.KindRN {
			return result, fmt.Errorf("kind must be bn or rn")
		}
	}

	if raw := strings.TrimSpace(values.Get("quality")); raw != "" {
		result.TimestampQuality = model.TimestampQuality(strings.ToLower(raw))
		if !validTimestampQuality(result.TimestampQuality) {
			return result, fmt.Errorf("unsupported timestamp quality %q", raw)
		}
	}

	if raw := strings.TrimSpace(values.Get("status")); raw != "" {
		result.Status = model.Status(strings.ToLower(raw))
		if !validStatus(result.Status) {
			return result, fmt.Errorf("unsupported device status %q", raw)
		}
	}

	if raw := strings.TrimSpace(values.Get("since")); raw != "" {
		since, err := parseRecentSince(raw, now)
		if err != nil {
			return result, err
		}
		result.Since = since
	} else if raw := strings.TrimSpace(values.Get("since_seconds")); raw != "" {
		seconds, err := strconv.ParseFloat(raw, 64)
		if err != nil || seconds < 0 || seconds > 365*24*60*60 {
			return result, fmt.Errorf("since_seconds must be from 0 to 31536000")
		}
		if seconds > 0 {
			result.Since = now.Add(-time.Duration(seconds * float64(time.Second)))
		}
	}
	return result, nil
}

func parseRecentSince(raw string, now time.Time) (time.Time, error) {
	if duration, err := time.ParseDuration(raw); err == nil {
		if duration < 0 || duration > 365*24*time.Hour {
			return time.Time{}, fmt.Errorf("since duration must be between 0 and 8760h")
		}
		return now.Add(-duration).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("since must be a Go duration such as 10m or an RFC3339 timestamp")
	}
	if parsed.After(now.Add(time.Minute)) {
		return time.Time{}, fmt.Errorf("since timestamp cannot be in the future")
	}
	return parsed.UTC(), nil
}

func validTimestampQuality(value model.TimestampQuality) bool {
	switch value {
	case model.TimestampSource, model.TimestampMissing, model.TimestampInvalidPast, model.TimestampFuture, model.TimestampRegressed:
		return true
	default:
		return false
	}
}

func validStatus(value model.Status) bool {
	switch value {
	case model.StatusOnlineObserved, model.StatusOfflineReported, model.StatusStale,
		model.StatusUnknownUpstream, model.StatusConflict, model.StatusDisconnected, model.StatusUnknown:
		return true
	default:
		return false
	}
}
