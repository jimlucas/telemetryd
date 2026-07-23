package state

import (
	"container/heap"
	"sort"
	"time"

	"telemetryd/internal/model"
)

const maxAttentionLimit = 500

type rnAttentionCandidate struct {
	rn     *rnState
	status statusResult
}

// rnAttentionHeap puts the least important selected candidate at the root.
type rnAttentionHeap []rnAttentionCandidate

func (h rnAttentionHeap) Len() int { return len(h) }
func (h rnAttentionHeap) Less(i, j int) bool {
	return rnAttentionBetter(h[j], h[i])
}
func (h rnAttentionHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *rnAttentionHeap) Push(value any) {
	*h = append(*h, value.(rnAttentionCandidate))
}
func (h *rnAttentionHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// RNAttention returns a bounded operator view ordered by diagnostic urgency.
// It scans the current inventory but allocates full RN views only for the
// selected records, avoiding the full-fleet response used by /v1/rns.
func (s *Store) RNAttention(now time.Time, limit int) RNSelection {
	now = now.UTC()
	if limit <= 0 {
		limit = 40
	}
	if limit > maxAttentionLimit {
		limit = maxAttentionLimit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bnStatuses := make(map[string]statusResult, len(s.bns))
	for id, bn := range s.bns {
		bnStatuses[id] = s.bnStatusLocked(bn, now)
	}
	selected := make(rnAttentionHeap, 0, limit)
	result := RNSelection{GeneratedAt: now, Total: len(s.rns)}
	for _, rn := range s.rns {
		candidate := rnAttentionCandidate{rn: rn, status: s.rnStatusLocked(rn, bnStatuses, now)}
		if candidate.status.Status != model.StatusOnlineObserved {
			result.ProblemCount++
		}
		if len(selected) < limit {
			heap.Push(&selected, candidate)
			continue
		}
		if rnAttentionBetter(candidate, selected[0]) {
			selected[0] = candidate
			heap.Fix(&selected, 0)
		}
	}

	sort.Slice(selected, func(i, j int) bool { return rnAttentionBetter(selected[i], selected[j]) })
	result.Items = make([]RNView, 0, len(selected))
	for _, candidate := range selected {
		result.Items = append(result.Items, s.rnViewLocked(candidate.rn, candidate.status, now, false))
	}
	result.Returned = len(result.Items)
	return result
}

func rnAttentionBetter(left, right rnAttentionCandidate) bool {
	leftRank := rnAttentionRank(left.status.Status)
	rightRank := rnAttentionRank(right.status.Status)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if !left.rn.LastSeenAt.Equal(right.rn.LastSeenAt) {
		return left.rn.LastSeenAt.Before(right.rn.LastSeenAt)
	}
	return left.rn.ID < right.rn.ID
}

func rnAttentionRank(status model.Status) int {
	switch status {
	case model.StatusConflict:
		return 0
	case model.StatusOfflineReported:
		return 1
	case model.StatusUnknownUpstream:
		return 2
	case model.StatusStale:
		return 3
	case model.StatusDisconnected:
		return 4
	case model.StatusUnknown:
		return 5
	case model.StatusOnlineObserved:
		return 6
	default:
		return 7
	}
}
