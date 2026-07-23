package state

import (
	"container/heap"
	"sort"
	"strings"
	"time"

	"telemetryd/internal/model"
)

const (
	defaultRecentLimit = 200
	maxRecentLimit     = 1_000
)

type recentCandidate struct {
	kind       model.DeviceKind
	deviceID   string
	hostname   string
	parentBNID string
	status     statusResult
	metric     *model.Metric
}

// recentMinHeap keeps the oldest selected item at index zero so a bounded scan
// can retain only the newest N current records without sorting every metric in
// a large deployment.
type recentMinHeap []recentCandidate

func (h recentMinHeap) Len() int { return len(h) }
func (h recentMinHeap) Less(i, j int) bool {
	return recentCandidateOlder(h[i], h[j])
}
func (h recentMinHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *recentMinHeap) Push(value any) {
	*h = append(*h, value.(recentCandidate))
}
func (h *recentMinHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// Recent returns a newest-first view of the latest retained value for each
// matched device/path. It deliberately does not create or imply event history.
func (s *Store) Recent(now time.Time, query RecentQuery) RecentRecords {
	now = now.UTC()
	query = normalizeRecentQuery(query)

	s.mu.RLock()
	defer s.mu.RUnlock()

	bnStatuses := make(map[string]statusResult, len(s.bns))
	for id, bn := range s.bns {
		bnStatuses[id] = s.bnStatusLocked(bn, now)
	}

	selected := make(recentMinHeap, 0, query.Limit)
	result := RecentRecords{GeneratedAt: now}
	consider := func(candidate recentCandidate) {
		result.Scanned++
		if !recentCandidateMatches(candidate, query) {
			return
		}
		result.Matched++
		if len(selected) < query.Limit {
			heap.Push(&selected, candidate)
			return
		}
		if recentCandidateOlder(selected[0], candidate) {
			selected[0] = candidate
			heap.Fix(&selected, 0)
		}
	}

	if query.Kind == "" || query.Kind == model.KindBN {
		for _, bn := range s.bns {
			status := bnStatuses[bn.ID]
			for _, metric := range bn.Metrics {
				consider(recentCandidate{
					kind: model.KindBN, deviceID: bn.ID, hostname: bn.Hostname,
					status: status, metric: metric,
				})
			}
		}
	}
	if query.Kind == "" || query.Kind == model.KindRN {
		for _, rn := range s.rns {
			status := s.rnStatusLocked(rn, bnStatuses, now)
			for _, metric := range rn.Metrics {
				consider(recentCandidate{
					kind: model.KindRN, deviceID: rn.ID, hostname: rn.Hostname,
					parentBNID: rn.ParentBNID, status: status, metric: metric,
				})
			}
		}
	}

	result.Items = make([]CurrentRecordView, 0, len(selected))
	for _, candidate := range selected {
		metric := cloneMetric(*candidate.metric)
		view := MetricView{Metric: metric, AgeSeconds: ageSeconds(now, metric.ReceivedAt)}
		if metric.SourceTimestamp != nil {
			age := ageSeconds(now, *metric.SourceTimestamp)
			view.SourceAgeSeconds = &age
		}
		result.Items = append(result.Items, CurrentRecordView{
			Kind: candidate.kind, DeviceID: candidate.deviceID, Hostname: candidate.hostname,
			ParentBNID: candidate.parentBNID, Status: candidate.status.Status,
			StatusCode: model.StatusCode(candidate.status.Status), StatusReason: candidate.status.Reason,
			MetricView: view,
		})
	}
	sort.Slice(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		if !left.ReceivedAt.Equal(right.ReceivedAt) {
			return left.ReceivedAt.After(right.ReceivedAt)
		}
		if left.ObservationOrder != right.ObservationOrder {
			return left.ObservationOrder > right.ObservationOrder
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.DeviceID != right.DeviceID {
			return left.DeviceID < right.DeviceID
		}
		return left.Path < right.Path
	})
	result.Returned = len(result.Items)
	result.Truncated = result.Matched > result.Returned
	return result
}

func normalizeRecentQuery(query RecentQuery) RecentQuery {
	if query.Limit <= 0 {
		query.Limit = defaultRecentLimit
	}
	if query.Limit > maxRecentLimit {
		query.Limit = maxRecentLimit
	}
	query.DeviceID = strings.TrimSpace(query.DeviceID)
	query.BNID = strings.TrimSpace(query.BNID)
	query.PathContains = strings.ToLower(strings.TrimSpace(query.PathContains))
	query.Search = strings.ToLower(strings.TrimSpace(query.Search))
	if !query.Since.IsZero() {
		query.Since = query.Since.UTC()
	}
	return query
}

func recentCandidateMatches(candidate recentCandidate, query RecentQuery) bool {
	metric := candidate.metric
	if metric == nil {
		return false
	}
	if query.DeviceID != "" && candidate.deviceID != query.DeviceID {
		return false
	}
	if query.BNID != "" {
		candidateBN := candidate.parentBNID
		if candidate.kind == model.KindBN {
			candidateBN = candidate.deviceID
		}
		if candidateBN != query.BNID && metric.SourceBNID != query.BNID {
			return false
		}
	}
	if query.TimestampQuality != "" && metric.TimestampQuality != query.TimestampQuality {
		return false
	}
	if query.Status != "" && candidate.status.Status != query.Status {
		return false
	}
	if !query.Since.IsZero() && metric.ReceivedAt.Before(query.Since) {
		return false
	}
	if query.PathContains != "" && !strings.Contains(strings.ToLower(metric.Path), query.PathContains) {
		return false
	}
	if query.Search != "" {
		haystack := strings.ToLower(strings.Join([]string{
			string(candidate.kind), candidate.deviceID, candidate.hostname, candidate.parentBNID,
			string(candidate.status.Status), metric.Path, metric.BasePath, metric.ValueText,
			metric.ValueType, string(metric.TimestampQuality), metric.StreamID, metric.ScopeID,
			metric.SourceBNID,
		}, "\x00"))
		if !strings.Contains(haystack, query.Search) {
			return false
		}
	}
	return true
}

func recentCandidateOlder(left, right recentCandidate) bool {
	if !left.metric.ReceivedAt.Equal(right.metric.ReceivedAt) {
		return left.metric.ReceivedAt.Before(right.metric.ReceivedAt)
	}
	if left.metric.ObservationOrder != right.metric.ObservationOrder {
		return left.metric.ObservationOrder < right.metric.ObservationOrder
	}
	// Records from one gNMI Notification legitimately share receive time and
	// observation order. Treat reverse lexical order as less desirable so a
	// limited response remains deterministic instead of depending on Go map
	// iteration order.
	return recentCandidateKey(left) > recentCandidateKey(right)
}

func recentCandidateKey(candidate recentCandidate) string {
	return string(candidate.kind) + "\x00" + candidate.deviceID + "\x00" + candidate.metric.Path
}
