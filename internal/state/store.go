package state

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"telemetryd/internal/model"
	"telemetryd/internal/pathutil"
)

type parentCandidate struct {
	LastSeenAt time.Time
	Order      uint64
}

type bnState struct {
	ID                        string
	IdentityQuality           model.IdentityQuality
	SNMPIndex                 uint32
	Hostname                  string
	MACAddress                string
	FirstSeenAt               time.Time
	LastSeenAt                time.Time
	LastSourceTimestamp       *time.Time
	TimestampQuality          model.TimestampQuality
	Metrics                   map[string]*model.Metric
	ActiveStreams             map[string]struct{}
	ReportedActiveConnections *int64
}

type rnState struct {
	ID                   string
	SNMPIndex            uint32
	Hostname             string
	MACAddress           string
	ParentBNID           string
	FirstSeenAt          time.Time
	LastSeenAt           time.Time
	LastEvidenceOrder    uint64
	LastSourceTimestamp  *time.Time
	TimestampQuality     model.TimestampQuality
	Metrics              map[string]*model.Metric
	ExplicitState        *model.ExplicitState
	ParentCandidates     map[string]parentCandidate
	LastDeleteAt         *time.Time
	LastAtomicOmissionAt *time.Time
}

type Store struct {
	mu sync.RWMutex

	cfg       Config
	startedAt time.Time
	bns       map[string]*bnState
	rns       map[string]*rnState
	rnsByBN   map[string]map[string]struct{}
	sessions  map[string]*SessionRecord
	stats     Stats
	nextOrder uint64

	indexOwners map[uint32]string
}

func New(cfg Config, now time.Time) *Store {
	defaults := DefaultConfig()
	if cfg.BNStaleAfter <= 0 {
		cfg.BNStaleAfter = defaults.BNStaleAfter
	}
	if cfg.RNStaleAfter <= 0 {
		cfg.RNStaleAfter = defaults.RNStaleAfter
	}
	if cfg.ParentConflictWindow <= 0 {
		cfg.ParentConflictWindow = defaults.ParentConflictWindow
	}
	if cfg.MaxFutureSkew <= 0 {
		cfg.MaxFutureSkew = defaults.MaxFutureSkew
	}
	if cfg.MinSourceTime.IsZero() {
		cfg.MinSourceTime = defaults.MinSourceTime
	}
	if cfg.MaxMetricsPerDevice <= 0 {
		cfg.MaxMetricsPerDevice = defaults.MaxMetricsPerDevice
	}
	if cfg.MaxBNs <= 0 {
		cfg.MaxBNs = defaults.MaxBNs
	}
	if cfg.MaxRNs <= 0 {
		cfg.MaxRNs = defaults.MaxRNs
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaults.MaxSessions
	}
	now = now.UTC()
	return &Store{
		cfg:         cfg,
		startedAt:   now,
		bns:         make(map[string]*bnState),
		rns:         make(map[string]*rnState),
		rnsByBN:     make(map[string]map[string]struct{}),
		sessions:    make(map[string]*SessionRecord),
		stats:       Stats{StartedAt: now},
		indexOwners: make(map[uint32]string),
	}
}

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) OpenSession(meta model.SessionMeta, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneSessionsLocked()
	id := randomID()
	for {
		if _, exists := s.sessions[id]; !exists {
			break
		}
		id = randomID()
	}
	now = now.UTC()
	s.sessions[id] = &SessionRecord{
		ID:          id,
		Meta:        cloneSessionMeta(meta),
		ConnectedAt: now,
		Active:      true,
	}
	s.stats.OpenedStreams++
	return id
}

func (s *Store) CloseSession(sessionID, reason string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || !session.Active {
		return
	}
	now = now.UTC()
	session.Active = false
	session.DisconnectedAt = timePtr(now)
	session.CloseReason = strings.TrimSpace(reason)
	if bn := s.bns[session.BNID]; bn != nil {
		delete(bn.ActiveStreams, sessionID)
	}
	s.stats.ClosedStreams++
}

func (s *Store) RecordMessage(sessionID string, now time.Time) (uint64, uint64, SessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return 0, 0, SessionRecord{}, false
	}
	now = now.UTC()
	session.MessageSequence++
	session.LastMessageAt = now
	s.nextOrder++
	s.stats.Messages++
	return session.MessageSequence, s.nextOrder, cloneSession(*session), true
}

func (s *Store) RecordSync(sessionID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		now = now.UTC()
		session.SyncCount++
		session.LastSyncAt = timePtr(now)
		s.stats.SyncResponses++
	}
}

func (s *Store) RecordDecodeError(sessionID, message string, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		session.DecodeErrors++
		session.LastError = message
	}
	s.stats.DecodeErrors++
}

func (s *Store) RecordProtocolError(sessionID, message string, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session := s.sessions[sessionID]; session != nil {
		session.ProtocolErrors++
		session.LastError = message
	}
	s.stats.ProtocolErrors++
}

func (s *Store) Apply(batch model.NotificationBatch) error {
	if strings.TrimSpace(batch.BN.ID) == "" {
		return errors.New("notification has no usable BN identity")
	}
	batch.ScopeID = strings.TrimSpace(batch.ScopeID)
	if batch.ScopeID == "" {
		// Direct Store callers and senders without subscription-name remain
		// conservative: atomic ownership is limited to this connection.
		batch.ScopeID = batch.SessionID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.sessions[batch.SessionID]
	if session == nil {
		return fmt.Errorf("unknown stream %q", batch.SessionID)
	}
	bn, err := s.ensureBNLocked(batch.BN, batch.ReceivedAt)
	if err != nil {
		return err
	}
	s.bindSessionLocked(session, bn, batch.BN)
	bn.LastSeenAt = maxTime(bn.LastSeenAt, batch.ReceivedAt.UTC())
	bn.ActiveStreams[batch.SessionID] = struct{}{}

	quality, sourceTime := s.timestampQualityLocked(batch.SourceTimestampNS, batch.ReceivedAt, nil)
	s.recordTimestampQualityLocked(quality)
	bn.TimestampQuality = quality
	if sourceTime != nil {
		bn.LastSourceTimestamp = cloneTimePtr(sourceTime)
	}

	session.NotificationCount++
	session.UpdateCount += uint64(len(batch.Updates))
	session.DeleteCount += uint64(len(batch.Deletes))
	s.stats.Notifications++
	s.stats.Updates += uint64(len(batch.Updates))
	s.stats.Deletes += uint64(len(batch.Deletes))
	if batch.Atomic {
		s.stats.AtomicNotifications++
	}

	present := make(map[string]struct{}, len(batch.Updates))
	updatedRNs := make(map[string]struct{})
	var applyErrors []error
	for _, observation := range batch.Updates {
		present[deviceMetricKey(observation.RNID, observation.CanonicalPath)] = struct{}{}
		if err := s.applyObservationLocked(bn, batch, observation); err != nil {
			applyErrors = append(applyErrors, fmt.Errorf("apply update %s: %w", observation.CanonicalPath, err))
			continue
		}
		if observation.RNID != "" {
			updatedRNs[observation.RNID] = struct{}{}
		}
	}

	for _, deletion := range batch.Deletes {
		if err := s.applyDeleteLocked(bn, batch, deletion); err != nil {
			applyErrors = append(applyErrors, fmt.Errorf("apply delete %s: %w", deletion.CanonicalPath, err))
		}
	}

	if batch.Atomic {
		s.applyAtomicLocked(bn, batch, present, updatedRNs)
	}
	return errors.Join(applyErrors...)
}

func (s *Store) ensureBNLocked(identity model.Identity, now time.Time) (*bnState, error) {
	id := strings.TrimSpace(identity.ID)
	if existing := s.bns[id]; existing != nil {
		if model.IdentityRank(identity.Quality) > model.IdentityRank(existing.IdentityQuality) {
			existing.IdentityQuality = identity.Quality
		}
		return existing, nil
	}
	if len(s.bns) >= s.cfg.MaxBNs {
		s.stats.RejectedDevices++
		return nil, fmt.Errorf("BN capacity %d reached; refusing %q", s.cfg.MaxBNs, id)
	}
	now = now.UTC()
	bn := &bnState{
		ID:               id,
		IdentityQuality:  identity.Quality,
		SNMPIndex:        s.allocateIndexLocked("bn:" + id),
		FirstSeenAt:      now,
		LastSeenAt:       now,
		TimestampQuality: model.TimestampMissing,
		Metrics:          make(map[string]*model.Metric),
		ActiveStreams:    make(map[string]struct{}),
	}
	s.bns[id] = bn
	return bn, nil
}

func (s *Store) ensureRNLocked(id string, now time.Time) (*rnState, error) {
	id = strings.TrimSpace(id)
	if existing := s.rns[id]; existing != nil {
		return existing, nil
	}
	if len(s.rns) >= s.cfg.MaxRNs {
		s.stats.RejectedDevices++
		return nil, fmt.Errorf("RN capacity %d reached; refusing %q", s.cfg.MaxRNs, id)
	}
	now = now.UTC()
	rn := &rnState{
		ID:               id,
		SNMPIndex:        s.allocateIndexLocked("rn:" + id),
		FirstSeenAt:      now,
		LastSeenAt:       now,
		TimestampQuality: model.TimestampMissing,
		Metrics:          make(map[string]*model.Metric),
		ParentCandidates: make(map[string]parentCandidate),
	}
	s.rns[id] = rn
	return rn, nil
}

func (s *Store) bindSessionLocked(session *SessionRecord, bn *bnState, identity model.Identity) {
	oldID := session.BNID
	if oldID != "" && oldID != bn.ID {
		if old := s.bns[oldID]; old != nil {
			if model.IdentityRank(identity.Quality) > model.IdentityRank(session.BNIDQuality) {
				s.mergeBNLocked(old, bn)
			} else {
				delete(old.ActiveStreams, session.ID)
			}
		}
	}
	if session.BNID == "" || model.IdentityRank(identity.Quality) >= model.IdentityRank(session.BNIDQuality) {
		session.BNID = bn.ID
		session.BNIDQuality = identity.Quality
	}
	bn.ActiveStreams[session.ID] = struct{}{}
}

func (s *Store) mergeBNLocked(old, target *bnState) {
	if old == nil || target == nil || old.ID == target.ID {
		return
	}
	if target.Hostname == "" {
		target.Hostname = old.Hostname
	}
	if target.MACAddress == "" {
		target.MACAddress = old.MACAddress
	}
	if target.FirstSeenAt.IsZero() || (!old.FirstSeenAt.IsZero() && old.FirstSeenAt.Before(target.FirstSeenAt)) {
		target.FirstSeenAt = old.FirstSeenAt
	}
	if old.LastSeenAt.After(target.LastSeenAt) {
		target.LastSeenAt = old.LastSeenAt
	}
	if target.LastSourceTimestamp == nil || (old.LastSourceTimestamp != nil && old.LastSourceTimestamp.After(*target.LastSourceTimestamp)) {
		target.LastSourceTimestamp = cloneTimePtr(old.LastSourceTimestamp)
		target.TimestampQuality = old.TimestampQuality
	}
	if target.ReportedActiveConnections == nil {
		target.ReportedActiveConnections = cloneInt64Ptr(old.ReportedActiveConnections)
	}
	for path, metric := range old.Metrics {
		existing := target.Metrics[path]
		if existing == nil || metric.ReceivedAt.After(existing.ReceivedAt) || (metric.ReceivedAt.Equal(existing.ReceivedAt) && metric.ObservationOrder > existing.ObservationOrder) {
			copy := cloneMetric(*metric)
			copy.SourceBNID = target.ID
			target.Metrics[path] = &copy
		}
	}
	for streamID := range old.ActiveStreams {
		target.ActiveStreams[streamID] = struct{}{}
		if session := s.sessions[streamID]; session != nil {
			session.BNID = target.ID
			if model.IdentityRank(target.IdentityQuality) > model.IdentityRank(session.BNIDQuality) {
				session.BNIDQuality = target.IdentityQuality
			}
		}
	}
	for rnID := range s.rnsByBN[old.ID] {
		rn := s.rns[rnID]
		if rn == nil {
			continue
		}
		oldCandidate, hadOld := rn.ParentCandidates[old.ID]
		newCandidate, hadNew := rn.ParentCandidates[target.ID]
		delete(rn.ParentCandidates, old.ID)
		if hadOld && (!hadNew || oldCandidate.Order > newCandidate.Order) {
			rn.ParentCandidates[target.ID] = oldCandidate
		}
		s.setRNParentLocked(rn, target.ID)
	}
	delete(s.rnsByBN, old.ID)
	delete(s.bns, old.ID)
	s.stats.IdentityMerges++
}

func (s *Store) applyObservationLocked(bn *bnState, batch model.NotificationBatch, observation model.Observation) error {
	if observation.CanonicalPath == "" {
		observation.CanonicalPath = pathutil.Canonical(observation.Path)
	}
	if observation.BasePath == "" {
		observation.BasePath = pathutil.Base(observation.Path)
	}

	if observation.RNID == "" {
		quality, sourceTime := s.timestampQualityLocked(batch.SourceTimestampNS, batch.ReceivedAt, bn.Metrics[observation.CanonicalPath])
		metric := s.upsertMetricLocked(bn.Metrics, observation, batch, bn.ID, quality, sourceTime)
		bn.TimestampQuality = metric.TimestampQuality
		if metric.SourceTimestamp != nil {
			bn.LastSourceTimestamp = cloneTimePtr(metric.SourceTimestamp)
		}
		if observation.Hints.Hostname != "" {
			bn.Hostname = observation.Hints.Hostname
		}
		if observation.Hints.MACAddress != "" {
			bn.MACAddress = observation.Hints.MACAddress
		}
		if observation.Hints.ActiveConnections != nil {
			value := *observation.Hints.ActiveConnections
			bn.ReportedActiveConnections = &value
		}
		s.evictMetricsLocked(bn.Metrics)
		return nil
	}

	rn, err := s.ensureRNLocked(observation.RNID, batch.ReceivedAt)
	if err != nil {
		return err
	}
	s.setRNParentLocked(rn, bn.ID)
	s.recordParentCandidateLocked(rn, bn.ID, batch.ReceivedAt.UTC(), batch.ObservationOrder)
	rn.LastSeenAt = batch.ReceivedAt.UTC()
	rn.LastEvidenceOrder = batch.ObservationOrder
	quality, sourceTime := s.timestampQualityLocked(batch.SourceTimestampNS, batch.ReceivedAt, rn.Metrics[observation.CanonicalPath])
	metric := s.upsertMetricLocked(rn.Metrics, observation, batch, bn.ID, quality, sourceTime)
	rn.TimestampQuality = metric.TimestampQuality
	if metric.SourceTimestamp != nil {
		rn.LastSourceTimestamp = cloneTimePtr(metric.SourceTimestamp)
	}
	if observation.Hints.Hostname != "" {
		rn.Hostname = observation.Hints.Hostname
	}
	if observation.Hints.MACAddress != "" {
		rn.MACAddress = observation.Hints.MACAddress
	}
	if observation.Hints.ExplicitState != "" {
		rn.ExplicitState = &model.ExplicitState{
			State:           observation.Hints.ExplicitState,
			Reason:          observation.Hints.ExplicitReason,
			Path:            observation.CanonicalPath,
			ReceivedAt:      batch.ReceivedAt.UTC(),
			SourceTimestamp: cloneTimePtr(sourceTime),
			Order:           batch.ObservationOrder,
		}
	}
	s.evictMetricsLocked(rn.Metrics)
	return nil
}

func (s *Store) upsertMetricLocked(metrics map[string]*model.Metric, observation model.Observation, batch model.NotificationBatch, bnID string, quality model.TimestampQuality, sourceTime *time.Time) *model.Metric {
	now := batch.ReceivedAt.UTC()
	old := metrics[observation.CanonicalPath]
	metric := &model.Metric{
		Path:               observation.CanonicalPath,
		BasePath:           observation.BasePath,
		Origin:             observation.Path.Origin,
		Target:             observation.Path.Target,
		Keys:               cloneStringMap(observation.Keys),
		Elements:           cloneElements(observation.Path.Elements),
		Value:              cloneValue(observation.Value.Data),
		ValueText:          observation.Value.Text,
		ValueType:          observation.Value.Type,
		FirstSeenAt:        now,
		ReceivedAt:         now,
		ChangedAt:          now,
		SourceTimestamp:    cloneTimePtr(sourceTime),
		SourceTimestampNS:  batch.SourceTimestampNS,
		TimestampQuality:   quality,
		StreamID:           batch.SessionID,
		ScopeID:            batch.ScopeID,
		MessageSequence:    batch.MessageSequence,
		ObservationOrder:   batch.ObservationOrder,
		SourceBNID:         bnID,
		Samples:            1,
		ReportedDuplicates: uint64(observation.ReportedDuplicates),
	}
	s.stats.ReportedDuplicates += uint64(observation.ReportedDuplicates)
	if old != nil {
		metric.FirstSeenAt = old.FirstSeenAt
		metric.Samples = old.Samples + 1
		metric.ValueChanges = old.ValueChanges
		metric.ExactDuplicates = old.ExactDuplicates
		metric.RepeatedValues = old.RepeatedValues
		metric.SourceRegressions = old.SourceRegressions
		metric.ReportedDuplicates += old.ReportedDuplicates
		if old.ValueType == metric.ValueType && old.ValueText == metric.ValueText {
			metric.ChangedAt = old.ChangedAt
			if old.SourceTimestampNS == metric.SourceTimestampNS && old.SourceTimestampNS != 0 {
				metric.ExactDuplicates++
				s.stats.ExactDuplicates++
			} else {
				metric.RepeatedValues++
				s.stats.RepeatedValues++
			}
		} else {
			metric.ValueChanges++
			s.stats.ValueChanges++
		}
		if batch.SourceTimestampNS > 0 && old.SourceTimestampNS > 0 && batch.SourceTimestampNS < old.SourceTimestampNS {
			metric.TimestampQuality = model.TimestampRegressed
			metric.SourceRegressions++
			s.stats.SourceRegressions++
		}
	}
	metrics[observation.CanonicalPath] = metric
	return metric
}

func (s *Store) applyDeleteLocked(bn *bnState, batch model.NotificationBatch, deletion model.Deletion) error {
	if deletion.RNID != "" {
		rn, err := s.ensureRNLocked(deletion.RNID, batch.ReceivedAt)
		if err != nil {
			return err
		}
		removed := removeUnder(rn.Metrics, deletion.Path.Elements)
		if removed > 0 {
			s.stats.DeletedMetrics += uint64(removed)
		}
		now := batch.ReceivedAt.UTC()
		quality, sourceTime := s.timestampQualityLocked(batch.SourceTimestampNS, batch.ReceivedAt, nil)
		rn.LastSeenAt = now
		rn.TimestampQuality = quality
		if sourceTime != nil {
			rn.LastSourceTimestamp = cloneTimePtr(sourceTime)
		}
		rn.LastDeleteAt = timePtr(now)
		s.setRNParentLocked(rn, bn.ID)
		s.recordParentCandidateLocked(rn, bn.ID, now, batch.ObservationOrder)
		if deletion.ConnectionRoot {
			rn.ExplicitState = &model.ExplicitState{
				State:           "offline",
				Reason:          "gNMI delete removed the RN connection",
				Path:            deletion.CanonicalPath,
				ReceivedAt:      now,
				SourceTimestamp: cloneTimePtr(sourceTime),
				Order:           batch.ObservationOrder,
			}
			rn.LastEvidenceOrder = batch.ObservationOrder
		}
		return nil
	}
	s.stats.DeletedMetrics += uint64(removeUnder(bn.Metrics, deletion.Path.Elements))
	return nil
}

func (s *Store) applyAtomicLocked(bn *bnState, batch model.NotificationBatch, present map[string]struct{}, updatedRNs map[string]struct{}) {
	removed := removeAtomicMissing(bn.Metrics, "", batch.Prefix.Elements, batch.ScopeID, present)
	s.stats.AtomicRemovals += uint64(removed)

	for rnID := range s.rnsByBN[bn.ID] {
		rn := s.rns[rnID]
		if rn == nil {
			continue
		}
		removed = removeAtomicMissing(rn.Metrics, rn.ID, batch.Prefix.Elements, batch.ScopeID, present)
		if removed == 0 {
			continue
		}
		s.stats.AtomicRemovals += uint64(removed)
		now := batch.ReceivedAt.UTC()
		rn.LastAtomicOmissionAt = timePtr(now)
		if s.cfg.AtomicOmissionMeansOffline {
			if _, seen := updatedRNs[rn.ID]; !seen && len(rn.Metrics) == 0 {
				rn.ExplicitState = &model.ExplicitState{
					State:      "offline",
					Reason:     "RN omitted by an atomic notification",
					Path:       pathutil.Canonical(batch.Prefix),
					ReceivedAt: now,
					Order:      batch.ObservationOrder,
				}
			}
		}
	}
}

func (s *Store) timestampQualityLocked(sourceNS int64, receivedAt time.Time, old *model.Metric) (model.TimestampQuality, *time.Time) {
	if sourceNS == 0 {
		return model.TimestampMissing, nil
	}
	if sourceNS < 0 {
		return model.TimestampInvalidPast, nil
	}
	source := time.Unix(0, sourceNS).UTC()
	quality := model.TimestampSource
	if source.Before(s.cfg.MinSourceTime) {
		quality = model.TimestampInvalidPast
	} else if source.After(receivedAt.UTC().Add(s.cfg.MaxFutureSkew)) {
		quality = model.TimestampFuture
	} else if old != nil && old.SourceTimestampNS > 0 && sourceNS < old.SourceTimestampNS {
		quality = model.TimestampRegressed
	}
	return quality, &source
}

func (s *Store) recordTimestampQualityLocked(quality model.TimestampQuality) {
	switch quality {
	case model.TimestampMissing:
		s.stats.MissingSourceTimestamp++
	case model.TimestampInvalidPast, model.TimestampFuture:
		s.stats.InvalidSourceTimestamp++
	}
}

func (s *Store) recordParentCandidateLocked(rn *rnState, bnID string, now time.Time, order uint64) {
	rn.ParentCandidates[bnID] = parentCandidate{LastSeenAt: now, Order: order}
	cutoff := now.Add(-s.cfg.ParentConflictWindow)
	for candidateID, candidate := range rn.ParentCandidates {
		if candidateID == bnID {
			continue
		}
		if candidate.LastSeenAt.Before(cutoff) {
			delete(rn.ParentCandidates, candidateID)
		}
	}
}

func (s *Store) setRNParentLocked(rn *rnState, bnID string) {
	if rn.ParentBNID == bnID {
		return
	}
	if rn.ParentBNID != "" {
		if members := s.rnsByBN[rn.ParentBNID]; members != nil {
			delete(members, rn.ID)
			if len(members) == 0 {
				delete(s.rnsByBN, rn.ParentBNID)
			}
		}
	}
	rn.ParentBNID = bnID
	if bnID != "" {
		members := s.rnsByBN[bnID]
		if members == nil {
			members = make(map[string]struct{})
			s.rnsByBN[bnID] = members
		}
		members[rn.ID] = struct{}{}
	}
}

func (s *Store) evictMetricsLocked(metrics map[string]*model.Metric) {
	for len(metrics) > s.cfg.MaxMetricsPerDevice {
		var oldestKey string
		var oldest time.Time
		for key, metric := range metrics {
			if oldestKey == "" || metric.ReceivedAt.Before(oldest) {
				oldestKey = key
				oldest = metric.ReceivedAt
			}
		}
		delete(metrics, oldestKey)
		s.stats.EvictedMetrics++
	}
}

func (s *Store) allocateIndexLocked(owner string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(owner))
	index := h.Sum32() & 0x7fffffff
	if index == 0 {
		index = 1
	}
	for {
		existing, used := s.indexOwners[index]
		if !used || existing == owner {
			s.indexOwners[index] = owner
			return index
		}
		index++
		index &= 0x7fffffff
		if index == 0 {
			index = 1
		}
	}
}

func (s *Store) pruneSessionsLocked() {
	if len(s.sessions) < s.cfg.MaxSessions {
		return
	}
	type candidate struct {
		id string
		at time.Time
	}
	closed := make([]candidate, 0, len(s.sessions))
	for id, session := range s.sessions {
		if session.Active {
			continue
		}
		at := session.ConnectedAt
		if session.DisconnectedAt != nil {
			at = *session.DisconnectedAt
		}
		closed = append(closed, candidate{id: id, at: at})
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].at.Before(closed[j].at) })
	for len(s.sessions) >= s.cfg.MaxSessions && len(closed) > 0 {
		delete(s.sessions, closed[0].id)
		closed = closed[1:]
	}
}

func removeUnder(metrics map[string]*model.Metric, prefix []model.PathElement) int {
	removed := 0
	for key, metric := range metrics {
		if pathutil.Under(metric.Elements, prefix) {
			delete(metrics, key)
			removed++
		}
	}
	return removed
}

func removeAtomicMissing(metrics map[string]*model.Metric, rnID string, prefix []model.PathElement, scopeID string, present map[string]struct{}) int {
	removed := 0
	for key, metric := range metrics {
		// Atomic completeness is scoped to the logical subscription that sent
		// the notification. subscription-name survives a reconnect; when it is
		// unavailable the ingestor deliberately falls back to the stream ID.
		if metric.ScopeID != scopeID || !pathutil.Under(metric.Elements, prefix) {
			continue
		}
		if _, ok := present[deviceMetricKey(rnID, key)]; ok {
			continue
		}
		delete(metrics, key)
		removed++
	}
	return removed
}

func deviceMetricKey(rnID, path string) string { return rnID + "\x00" + path }

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("stream-%d", time.Now().UnixNano())
}

func cloneSessionMeta(meta model.SessionMeta) model.SessionMeta {
	meta.Metadata = cloneStringMap(meta.Metadata)
	return meta
}

func cloneSession(session SessionRecord) SessionRecord {
	session.Meta = cloneSessionMeta(session.Meta)
	session.DisconnectedAt = cloneTimePtr(session.DisconnectedAt)
	session.LastSyncAt = cloneTimePtr(session.LastSyncAt)
	return session
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneElements(in []model.PathElement) []model.PathElement {
	out := make([]model.PathElement, len(in))
	for idx, element := range in {
		out[idx] = model.PathElement{Name: element.Name, Keys: cloneStringMap(element.Keys)}
	}
	return out
}

func cloneValue(value any) any {
	// DecodeValue produces scalars, []any, or map[string]any. Recursively copy
	// the composite forms so API serialization never races a caller mutation.
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
