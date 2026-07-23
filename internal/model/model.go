package model

import "time"

type DeviceKind string

const (
	KindBN DeviceKind = "bn"
	KindRN DeviceKind = "rn"
)

type Status string

const (
	StatusOnlineObserved  Status = "online_observed"
	StatusOfflineReported Status = "offline_reported"
	StatusStale           Status = "stale_unconfirmed"
	StatusUnknownUpstream Status = "unknown_upstream"
	StatusConflict        Status = "conflict"
	StatusDisconnected    Status = "disconnected"
	StatusUnknown         Status = "unknown"
)

func StatusCode(status Status) int {
	switch status {
	case StatusOfflineReported, StatusDisconnected:
		return 0
	case StatusOnlineObserved:
		return 1
	case StatusStale:
		return 2
	case StatusUnknownUpstream:
		return 3
	case StatusConflict:
		return 4
	default:
		return 5
	}
}

type TimestampQuality string

const (
	TimestampSource      TimestampQuality = "source"
	TimestampMissing     TimestampQuality = "missing"
	TimestampInvalidPast TimestampQuality = "invalid_past"
	TimestampFuture      TimestampQuality = "future"
	TimestampRegressed   TimestampQuality = "regressed"
)

type IdentityQuality string

const (
	IdentityPeer     IdentityQuality = "peer"
	IdentityMetadata IdentityQuality = "metadata"
	IdentityTarget   IdentityQuality = "target"
	IdentitySerial   IdentityQuality = "serial"
)

func IdentityRank(q IdentityQuality) int {
	switch q {
	case IdentitySerial:
		return 4
	case IdentityTarget:
		return 3
	case IdentityMetadata:
		return 2
	case IdentityPeer:
		return 1
	default:
		return 0
	}
}

type Identity struct {
	ID      string          `json:"id"`
	Quality IdentityQuality `json:"quality"`
}

type PathElement struct {
	Name string            `json:"name"`
	Keys map[string]string `json:"keys,omitempty"`
}

type Path struct {
	Origin   string        `json:"origin,omitempty"`
	Target   string        `json:"target,omitempty"`
	Elements []PathElement `json:"elements"`
}

type DecodedValue struct {
	Type string `json:"type"`
	Data any    `json:"data"`
	Text string `json:"text"`
}

type ExplicitState struct {
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	Path            string     `json:"path"`
	ReceivedAt      time.Time  `json:"received_at"`
	SourceTimestamp *time.Time `json:"source_timestamp,omitempty"`
	Order           uint64     `json:"order"`
}

type Metric struct {
	Path               string            `json:"path"`
	BasePath           string            `json:"base_path"`
	Origin             string            `json:"origin,omitempty"`
	Target             string            `json:"target,omitempty"`
	Keys               map[string]string `json:"keys,omitempty"`
	Elements           []PathElement     `json:"-"`
	Value              any               `json:"value"`
	ValueText          string            `json:"value_text"`
	ValueType          string            `json:"value_type"`
	FirstSeenAt        time.Time         `json:"first_seen_at"`
	ReceivedAt         time.Time         `json:"received_at"`
	ChangedAt          time.Time         `json:"changed_at"`
	SourceTimestamp    *time.Time        `json:"source_timestamp,omitempty"`
	SourceTimestampNS  int64             `json:"source_timestamp_ns,omitempty"`
	TimestampQuality   TimestampQuality  `json:"timestamp_quality"`
	StreamID           string            `json:"stream_id"`
	ScopeID            string            `json:"scope_id"`
	MessageSequence    uint64            `json:"message_sequence"`
	ObservationOrder   uint64            `json:"observation_order"`
	SourceBNID         string            `json:"source_bn_id"`
	Samples            uint64            `json:"samples"`
	ValueChanges       uint64            `json:"value_changes"`
	ExactDuplicates    uint64            `json:"exact_duplicates"`
	RepeatedValues     uint64            `json:"repeated_values"`
	SourceRegressions  uint64            `json:"source_regressions"`
	ReportedDuplicates uint64            `json:"reported_duplicates"`
}

type ObservationHints struct {
	Hostname          string
	MACAddress        string
	ExplicitState     string
	ExplicitReason    string
	ActiveConnections *int64
}

type Observation struct {
	RNID               string
	Path               Path
	CanonicalPath      string
	BasePath           string
	Keys               map[string]string
	Value              DecodedValue
	ReportedDuplicates uint32
	Hints              ObservationHints
}

type Deletion struct {
	RNID           string
	Path           Path
	CanonicalPath  string
	BasePath       string
	ConnectionRoot bool
}

type NotificationBatch struct {
	SessionID         string
	ScopeID           string
	Method            string
	BN                Identity
	ReceivedAt        time.Time
	SourceTimestampNS int64
	Atomic            bool
	Prefix            Path
	Updates           []Observation
	Deletes           []Deletion
	MessageSequence   uint64
	ObservationOrder  uint64
}

type SessionMeta struct {
	Method        string            `json:"method"`
	Peer          string            `json:"peer"`
	PeerHost      string            `json:"peer_host"`
	ClientSubject string            `json:"client_subject,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}
