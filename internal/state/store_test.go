package state

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"telemetryd/internal/model"
	"telemetryd/internal/pathutil"
)

func TestLatestReceiveWinsAndSourceRegressionIsVisible(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{Peer: "10.0.0.1:1234", PeerHost: "10.0.0.1"}, start)

	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 30.0, start, start)
	laterReceive := start.Add(time.Minute)
	olderSource := start.Add(-time.Minute)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 20.0, laterReceive, olderSource)

	metric, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/dl-snr")
	if !ok {
		t.Fatal("metric not found")
	}
	if got := metric.ValueText; got != "20" {
		t.Fatalf("latest receive did not win: got %q", got)
	}
	if metric.TimestampQuality != model.TimestampRegressed {
		t.Fatalf("timestamp quality = %q, want %q", metric.TimestampQuality, model.TimestampRegressed)
	}
	if metric.SourceRegressions != 1 {
		t.Fatalf("source regressions = %d, want 1", metric.SourceRegressions)
	}
	if !metric.ReceivedAt.Equal(laterReceive) {
		t.Fatalf("received time = %s, want %s", metric.ReceivedAt, laterReceive)
	}
}

func TestMissingTimestampRetainedAndDuplicatesCounted(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)

	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/ul-snr", int64(17), start, time.Time{})
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/ul-snr", int64(17), start.Add(time.Second), time.Time{})

	metric, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/ul-snr")
	if !ok {
		t.Fatal("metric not found")
	}
	if metric.TimestampQuality != model.TimestampMissing || metric.SourceTimestamp != nil {
		t.Fatalf("missing source timestamp not preserved: quality=%q source=%v", metric.TimestampQuality, metric.SourceTimestamp)
	}
	if metric.RepeatedValues != 1 || metric.ExactDuplicates != 0 {
		t.Fatalf("duplicate accounting incorrect: repeated=%d exact=%d", metric.RepeatedValues, metric.ExactDuplicates)
	}
	if store.Summary(start.Add(time.Second)).Stats.MissingSourceTimestamp != 2 {
		t.Fatalf("missing timestamp stat should count notifications, not fields")
	}
}

func TestAvailabilityDistinguishesParentFailureFromExplicitDelete(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.BNStaleAfter = time.Minute
	cfg.RNStaleAfter = time.Minute
	store := New(cfg, start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	rn, ok := store.GetRN("RN-1", start.Add(30*time.Second), false)
	if !ok || rn.Status != model.StatusOnlineObserved {
		t.Fatalf("fresh RN status = %q, want online", rn.Status)
	}

	store.CloseSession(stream, "transport lost", start.Add(31*time.Second))
	rn, _ = store.GetRN("RN-1", start.Add(32*time.Second), false)
	if rn.Status != model.StatusUnknownUpstream {
		t.Fatalf("RN under disconnected BN = %q, want unknown_upstream", rn.Status)
	}

	stream2 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start.Add(33*time.Second))
	seq, order, _, ok := store.RecordMessage(stream2, start.Add(34*time.Second))
	if !ok {
		t.Fatal("record message")
	}
	connectionPath := testPath("connections", "connection")
	connectionPath.Elements[1].Keys = map[string]string{"device-id": "RN-1"}
	if err := store.Apply(model.NotificationBatch{
		SessionID:        stream2,
		BN:               model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt:       start.Add(34 * time.Second),
		MessageSequence:  seq,
		ObservationOrder: order,
		Deletes: []model.Deletion{{
			RNID:           "RN-1",
			Path:           connectionPath,
			CanonicalPath:  pathutil.Canonical(connectionPath),
			BasePath:       pathutil.Base(connectionPath),
			ConnectionRoot: true,
		}},
	}); err != nil {
		t.Fatalf("apply delete: %v", err)
	}
	rn, _ = store.GetRN("RN-1", start.Add(35*time.Second), false)
	if rn.Status != model.StatusOfflineReported {
		t.Fatalf("explicitly deleted RN = %q, want offline_reported", rn.Status)
	}
}

func TestRNStaleOnlyWhenParentIsHealthy(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.BNStaleAfter = 2 * time.Minute
	cfg.RNStaleAfter = time.Minute
	store := New(cfg, start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	// Keep the parent fresh without touching the RN.
	applyTestBatch(t, store, stream, "BN-1", "", "/system/state/uptime", uint64(100), start.Add(90*time.Second), start.Add(90*time.Second))
	rn, _ := store.GetRN("RN-1", start.Add(91*time.Second), false)
	if rn.Status != model.StatusStale {
		t.Fatalf("RN status = %q, want stale_unconfirmed", rn.Status)
	}
}

func TestParentConflict(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream1 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	stream2 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.2"}, start)
	applyTestBatch(t, store, stream1, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)
	applyTestBatch(t, store, stream2, "BN-2", "RN-1", "/connections/connection/state/dl-snr", 26.0, start.Add(time.Second), start.Add(time.Second))

	rn, _ := store.GetRN("RN-1", start.Add(2*time.Second), false)
	if rn.Status != model.StatusConflict {
		t.Fatalf("RN status = %q, want conflict (%s)", rn.Status, rn.StatusReason)
	}

	sequence, order, _, ok := store.RecordMessage(stream1, start.Add(3*time.Second))
	if !ok {
		t.Fatal("record delete message")
	}
	connectionPath := testPath("connections", "connection")
	connectionPath.Elements[1].Keys = map[string]string{"device-id": "RN-1"}
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream1, BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start.Add(3 * time.Second), SourceTimestampNS: start.Add(3 * time.Second).UnixNano(),
		MessageSequence: sequence, ObservationOrder: order,
		Deletes: []model.Deletion{{
			RNID: "RN-1", Path: connectionPath, CanonicalPath: pathutil.Canonical(connectionPath),
			BasePath: pathutil.Base(connectionPath), ConnectionRoot: true,
		}},
	}); err != nil {
		t.Fatalf("apply conflicting delete: %v", err)
	}
	rn, _ = store.GetRN("RN-1", start.Add(4*time.Second), false)
	if rn.Status != model.StatusConflict {
		t.Fatalf("one BN's delete masked contradictory healthy parent evidence: %q (%s)", rn.Status, rn.StatusReason)
	}
}

func TestAtomicOmissionInvalidatesCachedValueButDoesNotInventOffline(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	seq, order, _, _ := store.RecordMessage(stream, start.Add(time.Second))
	prefix := testPath("connections")
	if err := store.Apply(model.NotificationBatch{
		SessionID:         stream,
		BN:                model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt:        start.Add(time.Second),
		SourceTimestampNS: start.Add(time.Second).UnixNano(),
		Atomic:            true,
		Prefix:            prefix,
		MessageSequence:   seq,
		ObservationOrder:  order,
	}); err != nil {
		t.Fatalf("atomic apply: %v", err)
	}
	if _, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/dl-snr"); ok {
		t.Fatal("omitted path remained in atomic subtree")
	}
	rn, _ := store.GetRN("RN-1", start.Add(2*time.Second), false)
	if rn.Status == model.StatusOfflineReported {
		t.Fatalf("atomic omission invented a definitive offline state: %s", rn.StatusReason)
	}
	if rn.LastAtomicOmissionAt == nil {
		t.Fatal("atomic omission was not recorded")
	}
}

func TestAtomicOmissionDoesNotEraseAnotherSubscriptionScope(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream1 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	stream2 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatchWithScope(t, store, stream1, "subscription-a", "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	prefix := testPath("connections")
	sequence, order, _, _ := store.RecordMessage(stream2, start.Add(time.Second))
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream2, ScopeID: "subscription-b", BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start.Add(time.Second), SourceTimestampNS: start.Add(time.Second).UnixNano(),
		Atomic: true, Prefix: prefix, MessageSequence: sequence, ObservationOrder: order,
	}); err != nil {
		t.Fatalf("atomic apply from second subscription: %v", err)
	}
	if _, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/dl-snr"); !ok {
		t.Fatal("atomic notification from another subscription scope erased a value it did not own")
	}
}

func TestAtomicOmissionSurvivesReconnectWithinSameSubscriptionScope(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream1 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	stream2 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start.Add(time.Second))
	applyTestBatchWithScope(t, store, stream1, "tarana-primary", "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	prefix := testPath("connections")
	sequence, order, _, _ := store.RecordMessage(stream2, start.Add(2*time.Second))
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream2, ScopeID: "tarana-primary", BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start.Add(2 * time.Second), SourceTimestampNS: start.Add(2 * time.Second).UnixNano(),
		Atomic: true, Prefix: prefix, MessageSequence: sequence, ObservationOrder: order,
	}); err != nil {
		t.Fatalf("atomic apply after reconnect: %v", err)
	}
	if _, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/dl-snr"); ok {
		t.Fatal("same logical subscription failed to invalidate its pre-reconnect value")
	}
}

func TestSenderReportedDuplicatesAreAggregated(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	sequence, order, _, ok := store.RecordMessage(stream, start)
	if !ok {
		t.Fatal("record message")
	}
	path := parseTestPath("/connections/connection/state/dl-snr", "RN-1")
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream, BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start, SourceTimestampNS: start.UnixNano(), MessageSequence: sequence, ObservationOrder: order,
		Updates: []model.Observation{{
			RNID: "RN-1", Path: path, CanonicalPath: pathutil.Canonical(path), BasePath: pathutil.Base(path),
			Keys: pathutil.FlattenKeys(path), Value: testValue(25.0), ReportedDuplicates: 3,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	metric, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/dl-snr")
	if !ok || metric.ReportedDuplicates != 3 {
		t.Fatalf("metric reported duplicates = %d, found=%v", metric.ReportedDuplicates, ok)
	}
	if got := store.Overview(start).Stats.ReportedDuplicates; got != 3 {
		t.Fatalf("collector reported duplicates = %d, want 3", got)
	}
}

func TestHigherQualityBNIdentityMergesTemporaryPeerIdentity(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatchWithQuality(t, store, stream, "10.0.0.1", model.IdentityPeer, "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)
	applyTestBatchWithQuality(t, store, stream, "BN-1", model.IdentityTarget, "RN-1", "/connections/connection/state/ul-snr", 20.0, start.Add(time.Second), start.Add(time.Second))

	snapshot := store.Snapshot(start.Add(2*time.Second), false)
	if len(snapshot.BNs) != 1 || snapshot.BNs[0].ID != "BN-1" {
		t.Fatalf("identity was not merged: %#v", snapshot.BNs)
	}
	if snapshot.RNs[0].ParentBNID != "BN-1" {
		t.Fatalf("RN parent was not migrated: %q", snapshot.RNs[0].ParentBNID)
	}
}

func TestBatchContinuesAfterOneRNCapacityRejection(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.MaxRNs = 1
	store := New(cfg, start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start, start)

	sequence, order, _, ok := store.RecordMessage(stream, start.Add(time.Second))
	if !ok {
		t.Fatal("record message")
	}
	makeObservation := func(rnID, canonical string, value float64) model.Observation {
		path := parseTestPath(canonical, rnID)
		return model.Observation{
			RNID: rnID, Path: path, CanonicalPath: pathutil.Canonical(path),
			BasePath: pathutil.Base(path), Keys: pathutil.FlattenKeys(path), Value: testValue(value),
		}
	}
	err := store.Apply(model.NotificationBatch{
		SessionID:         stream,
		BN:                model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt:        start.Add(time.Second),
		SourceTimestampNS: start.Add(time.Second).UnixNano(),
		MessageSequence:   sequence,
		ObservationOrder:  order,
		Updates: []model.Observation{
			makeObservation("RN-2", "/connections/connection/state/dl-snr", 18),
			makeObservation("RN-1", "/connections/connection/state/ul-snr", 20),
		},
	})
	if err == nil {
		t.Fatal("capacity rejection was not reported")
	}
	if _, ok := store.Lookup(model.KindRN, "RN-2", "/connections/connection/state/dl-snr"); ok {
		t.Fatal("rejected RN was created")
	}
	if metric, ok := store.Lookup(model.KindRN, "RN-1", "/connections/connection/state/ul-snr"); !ok || metric.ValueText != "20" {
		t.Fatalf("later existing-RN update was lost: metric=%#v found=%v", metric, ok)
	}
}

func TestExpiredParentCandidateDoesNotCreatePermanentConflict(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.BNStaleAfter = 10 * time.Minute
	cfg.ParentConflictWindow = time.Minute
	store := New(cfg, start)
	stream1 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	stream2 := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.2"}, start)
	applyTestBatch(t, store, stream1, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25, start, start)
	applyTestBatch(t, store, stream2, "BN-2", "RN-1", "/connections/connection/state/dl-snr", 26, start.Add(2*time.Minute), start.Add(2*time.Minute))

	rn, ok := store.GetRN("RN-1", start.Add(2*time.Minute+time.Second), false)
	if !ok {
		t.Fatal("RN missing")
	}
	if rn.Status == model.StatusConflict {
		t.Fatalf("expired parent candidate caused permanent conflict: %#v", rn.ParentCandidates)
	}
	if len(rn.ParentCandidates) != 1 || rn.ParentCandidates[0].BNID != "BN-2" {
		t.Fatalf("parent candidates were not pruned: %#v", rn.ParentCandidates)
	}
}

func TestLookupWithKeySelectorsDisambiguatesListMetrics(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)

	for index, value := range []float64{-55, -62} {
		received := start.Add(time.Duration(index) * time.Second)
		seq, order, _, ok := store.RecordMessage(stream, received)
		if !ok {
			t.Fatal("record message")
		}
		path := parseTestPath("/connections/connection/radios/radio/state/rx-signal-level/avg", "RN-1")
		path.Elements[3].Keys = map[string]string{"radio-id": strconv.Itoa(index)}
		if err := store.Apply(model.NotificationBatch{
			SessionID:         stream,
			BN:                model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
			ReceivedAt:        received,
			SourceTimestampNS: received.UnixNano(),
			MessageSequence:   seq,
			ObservationOrder:  order,
			Updates: []model.Observation{{
				RNID:          "RN-1",
				Path:          path,
				CanonicalPath: pathutil.Canonical(path),
				BasePath:      pathutil.Base(path),
				Keys:          pathutil.FlattenKeys(path),
				Value:         testValue(value),
			}},
		}); err != nil {
			t.Fatalf("apply radio %d: %v", index, err)
		}
	}

	base := "/connections/connection/radios/radio/state/rx-signal-level/avg"
	if _, ok := store.Lookup(model.KindRN, "RN-1", base); ok {
		t.Fatal("ambiguous base path unexpectedly resolved without a selector")
	}
	metric, ok := store.LookupWithKeys(model.KindRN, "RN-1", base, map[string]string{"radio_id": "1"})
	if !ok {
		t.Fatal("selector did not resolve radio metric")
	}
	if metric.ValueText != "-62" {
		t.Fatalf("resolved value = %q, want -62", metric.ValueText)
	}
}

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.BNStaleAfter = 5 * time.Minute
	cfg.RNStaleAfter = 5 * time.Minute
	return cfg
}

func applyTestBatch(t *testing.T, store *Store, stream, bnID, rnID, path string, value any, receive, source time.Time) {
	t.Helper()
	applyTestBatchWithQuality(t, store, stream, bnID, model.IdentityTarget, rnID, path, value, receive, source)
}

func applyTestBatchWithQuality(t *testing.T, store *Store, stream, bnID string, quality model.IdentityQuality, rnID, canonical string, value any, receive, source time.Time) {
	t.Helper()
	seq, order, _, ok := store.RecordMessage(stream, receive)
	if !ok {
		t.Fatal("record message")
	}
	path := parseTestPath(canonical, rnID)
	decoded := testValue(value)
	var sourceNS int64
	if !source.IsZero() {
		sourceNS = source.UnixNano()
	}
	if err := store.Apply(model.NotificationBatch{
		SessionID:         stream,
		BN:                model.Identity{ID: bnID, Quality: quality},
		ReceivedAt:        receive,
		SourceTimestampNS: sourceNS,
		MessageSequence:   seq,
		ObservationOrder:  order,
		Updates: []model.Observation{{
			RNID:          rnID,
			Path:          path,
			CanonicalPath: pathutil.Canonical(path),
			BasePath:      pathutil.Base(path),
			Keys:          pathutil.FlattenKeys(path),
			Value:         decoded,
		}},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func applyTestBatchWithScope(t *testing.T, store *Store, stream, scope, bnID, rnID, canonical string, value any, receive, source time.Time) {
	t.Helper()
	seq, order, _, ok := store.RecordMessage(stream, receive)
	if !ok {
		t.Fatal("record message")
	}
	path := parseTestPath(canonical, rnID)
	var sourceNS int64
	if !source.IsZero() {
		sourceNS = source.UnixNano()
	}
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream, ScopeID: scope, BN: model.Identity{ID: bnID, Quality: model.IdentityTarget},
		ReceivedAt: receive, SourceTimestampNS: sourceNS, MessageSequence: seq, ObservationOrder: order,
		Updates: []model.Observation{{
			RNID: rnID, Path: path, CanonicalPath: pathutil.Canonical(path), BasePath: pathutil.Base(path),
			Keys: pathutil.FlattenKeys(path), Value: testValue(value),
		}},
	}); err != nil {
		t.Fatalf("apply with scope: %v", err)
	}
}

func parseTestPath(canonical, rnID string) model.Path {
	parts := make([]string, 0)
	for _, part := range splitPath(canonical) {
		parts = append(parts, part)
	}
	path := testPath(parts...)
	if rnID != "" && len(path.Elements) > 1 && path.Elements[0].Name == "connections" && path.Elements[1].Name == "connection" {
		path.Elements[1].Keys = map[string]string{"device-id": rnID}
	}
	return path
}

func splitPath(value string) []string {
	var result []string
	start := 0
	for start < len(value) {
		for start < len(value) && value[start] == '/' {
			start++
		}
		if start >= len(value) {
			break
		}
		end := start
		for end < len(value) && value[end] != '/' {
			end++
		}
		result = append(result, value[start:end])
		start = end
	}
	return result
}

func testPath(names ...string) model.Path {
	result := model.Path{Elements: make([]model.PathElement, 0, len(names))}
	for _, name := range names {
		result.Elements = append(result.Elements, model.PathElement{Name: name})
	}
	return result
}

func testValue(value any) model.DecodedValue {
	switch typed := value.(type) {
	case float64:
		return model.DecodedValue{Type: "float64", Data: typed, Text: trimFloat(typed)}
	case int64:
		return model.DecodedValue{Type: "int64", Data: typed, Text: integerText(typed)}
	case uint64:
		return model.DecodedValue{Type: "uint64", Data: typed, Text: unsignedText(typed)}
	case string:
		return model.DecodedValue{Type: "string", Data: typed, Text: typed}
	default:
		return model.DecodedValue{Type: "unknown", Data: value, Text: "unknown"}
	}
}

func trimFloat(value float64) string   { return fmt.Sprintf("%g", value) }
func integerText(value int64) string   { return strconv.FormatInt(value, 10) }
func unsignedText(value uint64) string { return strconv.FormatUint(value, 10) }

func TestRecentReturnsOnlyLatestRetainedRecordPerDevicePath(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)

	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 20.0, start, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/ul-snr", 18.0, start.Add(time.Second), time.Time{})
	applyTestBatch(t, store, stream, "BN-1", "RN-1", "/connections/connection/state/dl-snr", 25.0, start.Add(2*time.Second), start.Add(2*time.Second))

	recent := store.Recent(start.Add(3*time.Second), RecentQuery{Kind: model.KindRN, Limit: 10})
	if recent.Scanned != 2 || recent.Matched != 2 || recent.Returned != 2 || recent.Truncated {
		t.Fatalf("recent metadata = %#v", recent)
	}
	if got := recent.Items[0]; got.DeviceID != "RN-1" || got.BasePath != "/connections/connection/state/dl-snr" || got.ValueText != "25" {
		t.Fatalf("newest record = %#v", got)
	}
	if recent.Items[0].Samples != 2 || recent.Items[0].ValueChanges != 1 {
		t.Fatalf("overwrite counters = %#v", recent.Items[0].Metric)
	}
	if recent.Items[1].TimestampQuality != model.TimestampMissing {
		t.Fatalf("missing source timestamp was not preserved: %#v", recent.Items[1])
	}

	filtered := store.Recent(start.Add(3*time.Second), RecentQuery{
		Kind: model.KindRN, Limit: 1, PathContains: "ul-snr", TimestampQuality: model.TimestampMissing,
	})
	if filtered.Matched != 1 || filtered.Returned != 1 || filtered.Items[0].BasePath != "/connections/connection/state/ul-snr" {
		t.Fatalf("filtered recent = %#v", filtered)
	}
}

func TestRecentLimitKeepsNewestRecords(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	for index, path := range []string{
		"/connections/connection/state/dl-snr",
		"/connections/connection/state/ul-snr",
		"/connections/connection/state/path-loss",
	} {
		received := start.Add(time.Duration(index) * time.Second)
		applyTestBatch(t, store, stream, "BN-1", "RN-1", path, float64(index), received, received)
	}

	recent := store.Recent(start.Add(10*time.Second), RecentQuery{Kind: model.KindRN, Limit: 2})
	if recent.Matched != 3 || recent.Returned != 2 || !recent.Truncated {
		t.Fatalf("bounded recent = %#v", recent)
	}
	if recent.Items[0].BasePath != "/connections/connection/state/path-loss" || recent.Items[1].BasePath != "/connections/connection/state/ul-snr" {
		t.Fatalf("wrong newest records: %#v", recent.Items)
	}
}

func TestExplicitOnlineDoesNotHideParentStreamLoss(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cfg := testConfig()
	cfg.BNStaleAfter = time.Minute
	cfg.RNStaleAfter = time.Minute
	store := New(cfg, start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	sequence, order, _, ok := store.RecordMessage(stream, start)
	if !ok {
		t.Fatal("record message")
	}
	path := parseTestPath("/connections/connection/state/connected", "RN-1")
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream, BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start, SourceTimestampNS: start.UnixNano(),
		MessageSequence: sequence, ObservationOrder: order,
		Updates: []model.Observation{{
			RNID: "RN-1", Path: path, CanonicalPath: pathutil.Canonical(path), BasePath: pathutil.Base(path),
			Keys: pathutil.FlattenKeys(path), Value: testValue("true"),
			Hints: model.ObservationHints{ExplicitState: "online", ExplicitReason: "connected=true"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if rn, _ := store.GetRN("RN-1", start.Add(time.Second), false); rn.Status != model.StatusOnlineObserved {
		t.Fatalf("fresh explicit-online RN = %q", rn.Status)
	}

	store.CloseSession(stream, "transport lost", start.Add(2*time.Second))
	rn, _ := store.GetRN("RN-1", start.Add(3*time.Second), false)
	if rn.Status != model.StatusUnknownUpstream {
		t.Fatalf("explicit online masked parent loss: status=%q reason=%q", rn.Status, rn.StatusReason)
	}
}

func TestRNAttentionPrioritizesProblemStateWithoutFullSnapshot(t *testing.T) {
	start := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := New(testConfig(), start)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-ONLINE", "/connections/connection/state/dl-snr", 25.0, start, start)
	applyTestBatch(t, store, stream, "BN-1", "RN-OFFLINE", "/connections/connection/state/dl-snr", 20.0, start.Add(time.Second), start.Add(time.Second))

	sequence, order, _, ok := store.RecordMessage(stream, start.Add(2*time.Second))
	if !ok {
		t.Fatal("record delete message")
	}
	connectionPath := testPath("connections", "connection")
	connectionPath.Elements[1].Keys = map[string]string{"device-id": "RN-OFFLINE"}
	if err := store.Apply(model.NotificationBatch{
		SessionID: stream, BN: model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt: start.Add(2 * time.Second), MessageSequence: sequence, ObservationOrder: order,
		Deletes: []model.Deletion{{
			RNID: "RN-OFFLINE", Path: connectionPath, CanonicalPath: pathutil.Canonical(connectionPath),
			BasePath: pathutil.Base(connectionPath), ConnectionRoot: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	selection := store.RNAttention(start.Add(3*time.Second), 1)
	if selection.Total != 2 || selection.ProblemCount != 1 || selection.Returned != 1 {
		t.Fatalf("attention metadata = %#v", selection)
	}
	if selection.Items[0].ID != "RN-OFFLINE" || selection.Items[0].Status != model.StatusOfflineReported {
		t.Fatalf("attention item = %#v", selection.Items[0])
	}
}
