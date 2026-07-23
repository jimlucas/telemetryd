package state

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"telemetryd/internal/model"
)

func (s *Store) Snapshot(now time.Time, includeMetrics bool) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(now.UTC(), includeMetrics)
}

func (s *Store) Overview(now time.Time) Overview {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := 0
	for _, session := range s.sessions {
		if session.Active {
			active++
		}
	}
	now = now.UTC()
	return Overview{
		GeneratedAt: now, UptimeSeconds: ageSeconds(now, s.startedAt),
		BNCount: len(s.bns), RNCount: len(s.rns), ActiveStreams: active, Stats: s.stats,
	}
}

func (s *Store) Summary(now time.Time) Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryLocked(now.UTC())
}

// summaryLocked computes fleet counts without constructing every BN/RN view.
// The dashboard polls this endpoint frequently, so avoiding a full snapshot
// allocation is important for large RN populations.
func (s *Store) summaryLocked(now time.Time) Summary {
	bnStatuses := make(map[string]statusResult, len(s.bns))
	summary := Summary{
		GeneratedAt: now, UptimeSeconds: ageSeconds(now, s.startedAt),
		BNCount: len(s.bns), RNCount: len(s.rns), Stats: s.stats,
	}
	for id, bn := range s.bns {
		status := s.bnStatusLocked(bn, now)
		bnStatuses[id] = status
		incrementStatus(&summary.BNStatuses, status.Status)
	}
	for _, rn := range s.rns {
		status := s.rnStatusLocked(rn, bnStatuses, now)
		incrementStatus(&summary.RNStatuses, status.Status)
	}
	for _, session := range s.sessions {
		if session.Active {
			summary.ActiveStreams++
		}
	}
	return summary
}

func (s *Store) BNs(now time.Time, includeMetrics bool) []BNView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(now.UTC(), includeMetrics).BNs
}

func (s *Store) RNs(now time.Time, includeMetrics bool) []RNView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(now.UTC(), includeMetrics).RNs
}

func (s *Store) ActiveStreamCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := 0
	for _, session := range s.sessions {
		if session.Active {
			active++
		}
	}
	return active
}

func (s *Store) Sessions() []SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]SessionRecord, 0, len(s.sessions))
	for _, session := range s.sessions {
		result = append(result, cloneSession(*session))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Active != result[j].Active {
			return result[i].Active
		}
		return result[i].ConnectedAt.After(result[j].ConnectedAt)
	})
	return result
}

func (s *Store) GetBN(id string, now time.Time, includeMetrics bool) (BNView, bool) {
	id = strings.TrimSpace(id)
	now = now.UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()

	bn := s.bns[id]
	if bn == nil {
		return BNView{}, false
	}
	bnStatuses := make(map[string]statusResult, len(s.bns))
	for candidateID, candidate := range s.bns {
		bnStatuses[candidateID] = s.bnStatusLocked(candidate, now)
	}
	fresh := 0
	for rnID := range s.rnsByBN[id] {
		rn := s.rns[rnID]
		if rn == nil {
			continue
		}
		if s.rnStatusLocked(rn, bnStatuses, now).Status == model.StatusOnlineObserved {
			fresh++
		}
	}
	return s.bnViewLocked(bn, bnStatuses[id], fresh, now, includeMetrics), true
}

func (s *Store) GetRN(id string, now time.Time, includeMetrics bool) (RNView, bool) {
	id = strings.TrimSpace(id)
	now = now.UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()

	rn := s.rns[id]
	if rn == nil {
		return RNView{}, false
	}
	bnStatuses := make(map[string]statusResult, len(rn.ParentCandidates)+1)
	if rn.ParentBNID != "" {
		if bn := s.bns[rn.ParentBNID]; bn != nil {
			bnStatuses[rn.ParentBNID] = s.bnStatusLocked(bn, now)
		}
	}
	for candidateID := range rn.ParentCandidates {
		if _, exists := bnStatuses[candidateID]; exists {
			continue
		}
		if bn := s.bns[candidateID]; bn != nil {
			bnStatuses[candidateID] = s.bnStatusLocked(bn, now)
		}
	}
	status := s.rnStatusLocked(rn, bnStatuses, now)
	return s.rnViewLocked(rn, status, now, includeMetrics), true
}

func (s *Store) Lookup(kind model.DeviceKind, id, path string) (model.Metric, bool) {
	return s.LookupWithKeys(kind, id, path, nil)
}

// LookupWithKeys resolves a latest-value metric by canonical path or by an
// unkeyed base path plus one or more path-key selectors. Selectors are useful
// for list-valued paths such as Tarana per-radio metrics, where the base path
// alone is intentionally considered ambiguous.
func (s *Store) LookupWithKeys(kind model.DeviceKind, id, path string, selectors map[string]string) (model.Metric, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path = strings.TrimSpace(path)
	id = strings.TrimSpace(id)

	var metrics map[string]*model.Metric
	switch kind {
	case model.KindBN:
		if device := s.bns[id]; device != nil {
			metrics = device.Metrics
		}
	case model.KindRN:
		if device := s.rns[id]; device != nil {
			metrics = device.Metrics
		}
	default:
		return model.Metric{}, false
	}
	if metrics == nil {
		return model.Metric{}, false
	}
	metric := lookupMetric(metrics, path, selectors)
	if metric == nil {
		return model.Metric{}, false
	}
	return cloneMetric(*metric), true
}

func (s *Store) snapshotLocked(now time.Time, includeMetrics bool) Snapshot {
	bnStatuses := make(map[string]statusResult, len(s.bns))
	for id, bn := range s.bns {
		bnStatuses[id] = s.bnStatusLocked(bn, now)
	}

	freshRNCount := make(map[string]int)
	rnViews := make([]RNView, 0, len(s.rns))
	for _, rn := range s.rns {
		status := s.rnStatusLocked(rn, bnStatuses, now)
		if status.Status == model.StatusOnlineObserved && rn.ParentBNID != "" {
			freshRNCount[rn.ParentBNID]++
		}
		view := RNView{
			ID:                   rn.ID,
			SNMPIndex:            rn.SNMPIndex,
			Hostname:             rn.Hostname,
			MACAddress:           rn.MACAddress,
			ParentBNID:           rn.ParentBNID,
			FirstSeenAt:          rn.FirstSeenAt,
			LastSeenAt:           rn.LastSeenAt,
			AgeSeconds:           ageSeconds(now, rn.LastSeenAt),
			LastSourceTimestamp:  cloneTimePtr(rn.LastSourceTimestamp),
			TimestampQuality:     rn.TimestampQuality,
			Status:               status.Status,
			StatusCode:           model.StatusCode(status.Status),
			StatusReason:         status.Reason,
			ExplicitState:        cloneExplicitState(rn.ExplicitState),
			ParentCandidates:     parentCandidateViews(rn.ParentCandidates, now),
			MetricCount:          len(rn.Metrics),
			LastDeleteAt:         cloneTimePtr(rn.LastDeleteAt),
			LastAtomicOmissionAt: cloneTimePtr(rn.LastAtomicOmissionAt),
		}
		if includeMetrics {
			view.Metrics = metricViews(rn.Metrics, now)
		}
		rnViews = append(rnViews, view)
	}
	sort.Slice(rnViews, func(i, j int) bool { return rnViews[i].ID < rnViews[j].ID })

	bnViews := make([]BNView, 0, len(s.bns))
	for _, bn := range s.bns {
		status := bnStatuses[bn.ID]
		streamIDs := make([]string, 0, len(bn.ActiveStreams))
		for id := range bn.ActiveStreams {
			if session := s.sessions[id]; session != nil && session.Active {
				streamIDs = append(streamIDs, id)
			}
		}
		sort.Strings(streamIDs)
		view := BNView{
			ID:                        bn.ID,
			IdentityQuality:           bn.IdentityQuality,
			SNMPIndex:                 bn.SNMPIndex,
			Hostname:                  bn.Hostname,
			MACAddress:                bn.MACAddress,
			FirstSeenAt:               bn.FirstSeenAt,
			LastSeenAt:                bn.LastSeenAt,
			AgeSeconds:                ageSeconds(now, bn.LastSeenAt),
			LastSourceTimestamp:       cloneTimePtr(bn.LastSourceTimestamp),
			TimestampQuality:          bn.TimestampQuality,
			Status:                    status.Status,
			StatusCode:                model.StatusCode(status.Status),
			StatusReason:              status.Reason,
			ActiveStreams:             len(streamIDs),
			StreamIDs:                 streamIDs,
			ReportedActiveConnections: cloneInt64Ptr(bn.ReportedActiveConnections),
			FreshRNCount:              freshRNCount[bn.ID],
			MetricCount:               len(bn.Metrics),
		}
		if bn.ReportedActiveConnections != nil {
			delta := *bn.ReportedActiveConnections - int64(view.FreshRNCount)
			view.ConnectionCountDelta = &delta
		}
		if includeMetrics {
			view.Metrics = metricViews(bn.Metrics, now)
		}
		bnViews = append(bnViews, view)
	}
	sort.Slice(bnViews, func(i, j int) bool { return bnViews[i].ID < bnViews[j].ID })

	streams := make([]SessionRecord, 0, len(s.sessions))
	activeStreams := 0
	for _, session := range s.sessions {
		copy := cloneSession(*session)
		streams = append(streams, copy)
		if copy.Active {
			activeStreams++
		}
	}
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].Active != streams[j].Active {
			return streams[i].Active
		}
		return streams[i].ConnectedAt.After(streams[j].ConnectedAt)
	})

	summary := Summary{
		GeneratedAt:   now,
		UptimeSeconds: ageSeconds(now, s.startedAt),
		BNCount:       len(bnViews),
		RNCount:       len(rnViews),
		ActiveStreams: activeStreams,
		Stats:         s.stats,
	}
	for _, bn := range bnViews {
		incrementStatus(&summary.BNStatuses, bn.Status)
	}
	for _, rn := range rnViews {
		incrementStatus(&summary.RNStatuses, rn.Status)
	}

	return Snapshot{Summary: summary, BNs: bnViews, RNs: rnViews, Streams: streams}
}

func (s *Store) bnViewLocked(bn *bnState, status statusResult, freshRNCount int, now time.Time, includeMetrics bool) BNView {
	streamIDs := make([]string, 0, len(bn.ActiveStreams))
	for id := range bn.ActiveStreams {
		if session := s.sessions[id]; session != nil && session.Active {
			streamIDs = append(streamIDs, id)
		}
	}
	sort.Strings(streamIDs)
	view := BNView{
		ID:                        bn.ID,
		IdentityQuality:           bn.IdentityQuality,
		SNMPIndex:                 bn.SNMPIndex,
		Hostname:                  bn.Hostname,
		MACAddress:                bn.MACAddress,
		FirstSeenAt:               bn.FirstSeenAt,
		LastSeenAt:                bn.LastSeenAt,
		AgeSeconds:                ageSeconds(now, bn.LastSeenAt),
		LastSourceTimestamp:       cloneTimePtr(bn.LastSourceTimestamp),
		TimestampQuality:          bn.TimestampQuality,
		Status:                    status.Status,
		StatusCode:                model.StatusCode(status.Status),
		StatusReason:              status.Reason,
		ActiveStreams:             len(streamIDs),
		StreamIDs:                 streamIDs,
		ReportedActiveConnections: cloneInt64Ptr(bn.ReportedActiveConnections),
		FreshRNCount:              freshRNCount,
		MetricCount:               len(bn.Metrics),
	}
	if bn.ReportedActiveConnections != nil {
		delta := *bn.ReportedActiveConnections - int64(freshRNCount)
		view.ConnectionCountDelta = &delta
	}
	if includeMetrics {
		view.Metrics = metricViews(bn.Metrics, now)
	}
	return view
}

func (s *Store) rnViewLocked(rn *rnState, status statusResult, now time.Time, includeMetrics bool) RNView {
	view := RNView{
		ID:                   rn.ID,
		SNMPIndex:            rn.SNMPIndex,
		Hostname:             rn.Hostname,
		MACAddress:           rn.MACAddress,
		ParentBNID:           rn.ParentBNID,
		FirstSeenAt:          rn.FirstSeenAt,
		LastSeenAt:           rn.LastSeenAt,
		AgeSeconds:           ageSeconds(now, rn.LastSeenAt),
		LastSourceTimestamp:  cloneTimePtr(rn.LastSourceTimestamp),
		TimestampQuality:     rn.TimestampQuality,
		Status:               status.Status,
		StatusCode:           model.StatusCode(status.Status),
		StatusReason:         status.Reason,
		ExplicitState:        cloneExplicitState(rn.ExplicitState),
		ParentCandidates:     parentCandidateViews(rn.ParentCandidates, now),
		MetricCount:          len(rn.Metrics),
		LastDeleteAt:         cloneTimePtr(rn.LastDeleteAt),
		LastAtomicOmissionAt: cloneTimePtr(rn.LastAtomicOmissionAt),
	}
	if includeMetrics {
		view.Metrics = metricViews(rn.Metrics, now)
	}
	return view
}

type statusResult struct {
	Status model.Status
	Reason string
}

func (s *Store) bnStatusLocked(bn *bnState, now time.Time) statusResult {
	active := 0
	for id := range bn.ActiveStreams {
		if session := s.sessions[id]; session != nil && session.Active {
			active++
		}
	}
	if active == 0 {
		return statusResult{Status: model.StatusDisconnected, Reason: "no active dial-out stream from BN"}
	}
	age := now.Sub(bn.LastSeenAt)
	if age > s.cfg.BNStaleAfter {
		return statusResult{
			Status: model.StatusStale,
			Reason: fmt.Sprintf("active stream exists, but no BN notification for %s", conciseDuration(age)),
		}
	}
	return statusResult{Status: model.StatusOnlineObserved, Reason: "dial-out stream is active and BN telemetry is fresh"}
}

func (s *Store) rnStatusLocked(rn *rnState, bnStatuses map[string]statusResult, now time.Time) statusResult {
	healthyParents := make([]string, 0, len(rn.ParentCandidates))
	for bnID, candidate := range rn.ParentCandidates {
		if now.Sub(candidate.LastSeenAt) > s.cfg.ParentConflictWindow {
			continue
		}
		if status, ok := bnStatuses[bnID]; ok && status.Status == model.StatusOnlineObserved {
			healthyParents = append(healthyParents, bnID)
		}
	}
	if len(healthyParents) > 1 {
		sort.Strings(healthyParents)
		return statusResult{
			Status: model.StatusConflict,
			Reason: "RN was recently reported by multiple healthy BNs: " + strings.Join(healthyParents, ", "),
		}
	}

	// Evaluate definitive offline evidence after contradictory parent claims but
	// before upstream health. A reported disconnect/delete remains meaningful
	// even if the BN stream subsequently drops. Positive online evidence is
	// different: it cannot prove present availability once the only reporting
	// path is disconnected or stale, so that case is evaluated after the parent.
	if rn.ExplicitState != nil && rn.ExplicitState.Order >= rn.LastEvidenceOrder {
		switch strings.ToLower(rn.ExplicitState.State) {
		case "offline", "down", "disconnected":
			return statusResult{Status: model.StatusOfflineReported, Reason: rn.ExplicitState.Reason}
		}
	}

	parent, ok := bnStatuses[rn.ParentBNID]
	if !ok || parent.Status == model.StatusDisconnected || parent.Status == model.StatusStale {
		reason := "RN parent BN is unknown"
		if ok {
			reason = fmt.Sprintf("parent BN %s is %s", rn.ParentBNID, parent.Status)
		}
		return statusResult{Status: model.StatusUnknownUpstream, Reason: reason}
	}

	if rn.ExplicitState != nil && rn.ExplicitState.Order >= rn.LastEvidenceOrder {
		switch strings.ToLower(rn.ExplicitState.State) {
		case "online", "up", "connected":
			if now.Sub(rn.ExplicitState.ReceivedAt) <= s.cfg.RNStaleAfter {
				return statusResult{Status: model.StatusOnlineObserved, Reason: rn.ExplicitState.Reason}
			}
		}
	}

	age := now.Sub(rn.LastSeenAt)
	if age <= s.cfg.RNStaleAfter {
		return statusResult{Status: model.StatusOnlineObserved, Reason: "RN telemetry is fresh and parent BN is healthy"}
	}
	return statusResult{
		Status: model.StatusStale,
		Reason: fmt.Sprintf("parent BN is healthy, but no RN telemetry for %s", conciseDuration(age)),
	}
}

func lookupMetric(metrics map[string]*model.Metric, path string, selectors map[string]string) *model.Metric {
	if len(selectors) == 0 {
		if metric := metrics[path]; metric != nil {
			return metric
		}
	}

	// Return a key-free base path only when it identifies exactly one metric.
	// Optional selectors disambiguate list entries without forcing monitoring
	// systems to construct escaped canonical gNMI paths.
	var match *model.Metric
	for _, metric := range metrics {
		if metric.BasePath != path && metric.Path != path {
			continue
		}
		if !metricMatchesSelectors(metric, selectors) {
			continue
		}
		if match != nil {
			return nil
		}
		match = metric
	}
	return match
}

func metricMatchesSelectors(metric *model.Metric, selectors map[string]string) bool {
	for key, wanted := range selectors {
		wanted = strings.TrimSpace(wanted)
		if wanted == "" {
			continue
		}
		actual, ok := metric.Keys[key]
		if !ok {
			// Accept punctuation variants (radio-id, radio_id, radioId) while
			// preserving exact matching whenever possible.
			normalized := normalizeSelectorKey(key)
			for candidateKey, candidateValue := range metric.Keys {
				if normalizeSelectorKey(candidateKey) == normalized {
					actual, ok = candidateValue, true
					break
				}
			}
		}
		if !ok || actual != wanted {
			return false
		}
	}
	return true
}

func normalizeSelectorKey(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func metricViews(metrics map[string]*model.Metric, now time.Time) []MetricView {
	result := make([]MetricView, 0, len(metrics))
	for _, metric := range metrics {
		view := MetricView{Metric: cloneMetric(*metric), AgeSeconds: ageSeconds(now, metric.ReceivedAt)}
		if metric.SourceTimestamp != nil {
			age := ageSeconds(now, *metric.SourceTimestamp)
			view.SourceAgeSeconds = &age
		}
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func cloneMetric(metric model.Metric) model.Metric {
	metric.Keys = cloneStringMap(metric.Keys)
	metric.Elements = cloneElements(metric.Elements)
	metric.Value = cloneValue(metric.Value)
	metric.SourceTimestamp = cloneTimePtr(metric.SourceTimestamp)
	return metric
}

func cloneExplicitState(state *model.ExplicitState) *model.ExplicitState {
	if state == nil {
		return nil
	}
	copy := *state
	copy.SourceTimestamp = cloneTimePtr(state.SourceTimestamp)
	return &copy
}

func parentCandidateViews(candidates map[string]parentCandidate, now time.Time) []ParentCandidateView {
	result := make([]ParentCandidateView, 0, len(candidates))
	for id, candidate := range candidates {
		result = append(result, ParentCandidateView{
			BNID:       id,
			LastSeenAt: candidate.LastSeenAt,
			AgeSeconds: ageSeconds(now, candidate.LastSeenAt),
			Order:      candidate.Order,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeenAt.Equal(result[j].LastSeenAt) {
			return result[i].BNID < result[j].BNID
		}
		return result[i].LastSeenAt.After(result[j].LastSeenAt)
	})
	return result
}

func incrementStatus(counts *StatusCounts, status model.Status) {
	switch status {
	case model.StatusOnlineObserved:
		counts.OnlineObserved++
	case model.StatusOfflineReported:
		counts.OfflineReported++
	case model.StatusStale:
		counts.Stale++
	case model.StatusUnknownUpstream:
		counts.UnknownUpstream++
	case model.StatusConflict:
		counts.Conflict++
	case model.StatusDisconnected:
		counts.Disconnected++
	default:
		counts.Unknown++
	}
}

func ageSeconds(now, then time.Time) float64 {
	if then.IsZero() {
		return 0
	}
	seconds := now.Sub(then).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

func conciseDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Round(time.Second).String()
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
