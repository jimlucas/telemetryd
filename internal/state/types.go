package state

import (
	"time"

	"telemetryd/internal/model"
)

type Config struct {
	BNStaleAfter               time.Duration
	RNStaleAfter               time.Duration
	ParentConflictWindow       time.Duration
	MaxFutureSkew              time.Duration
	MinSourceTime              time.Time
	MaxMetricsPerDevice        int
	MaxBNs                     int
	MaxRNs                     int
	MaxSessions                int
	AtomicOmissionMeansOffline bool
}

func DefaultConfig() Config {
	return Config{
		BNStaleAfter:         3 * time.Minute,
		RNStaleAfter:         5 * time.Minute,
		ParentConflictWindow: 10 * time.Minute,
		MaxFutureSkew:        5 * time.Minute,
		MinSourceTime:        time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		MaxMetricsPerDevice:  20_000,
		MaxBNs:               10_000,
		MaxRNs:               1_000_000,
		MaxSessions:          4_096,
	}
}

type Stats struct {
	StartedAt              time.Time `json:"started_at"`
	Messages               uint64    `json:"messages"`
	Notifications          uint64    `json:"notifications"`
	Updates                uint64    `json:"updates"`
	Deletes                uint64    `json:"deletes"`
	AtomicNotifications    uint64    `json:"atomic_notifications"`
	AtomicRemovals         uint64    `json:"atomic_removals"`
	DeletedMetrics         uint64    `json:"deleted_metrics"`
	ExactDuplicates        uint64    `json:"exact_duplicates"`
	RepeatedValues         uint64    `json:"repeated_values"`
	ReportedDuplicates     uint64    `json:"reported_duplicates"`
	ValueChanges           uint64    `json:"value_changes"`
	SourceRegressions      uint64    `json:"source_regressions"`
	MissingSourceTimestamp uint64    `json:"missing_source_timestamp"`
	InvalidSourceTimestamp uint64    `json:"invalid_source_timestamp"`
	DecodeErrors           uint64    `json:"decode_errors"`
	ProtocolErrors         uint64    `json:"protocol_errors"`
	RejectedDevices        uint64    `json:"rejected_devices"`
	IdentityMerges         uint64    `json:"identity_merges"`
	EvictedMetrics         uint64    `json:"evicted_metrics"`
	OpenedStreams          uint64    `json:"opened_streams"`
	ClosedStreams          uint64    `json:"closed_streams"`
	SyncResponses          uint64    `json:"sync_responses"`
}

type SessionRecord struct {
	ID                string                `json:"id"`
	Meta              model.SessionMeta     `json:"meta"`
	BNID              string                `json:"bn_id,omitempty"`
	BNIDQuality       model.IdentityQuality `json:"bn_id_quality,omitempty"`
	ConnectedAt       time.Time             `json:"connected_at"`
	LastMessageAt     time.Time             `json:"last_message_at,omitempty"`
	DisconnectedAt    *time.Time            `json:"disconnected_at,omitempty"`
	Active            bool                  `json:"active"`
	CloseReason       string                `json:"close_reason,omitempty"`
	MessageSequence   uint64                `json:"message_sequence"`
	NotificationCount uint64                `json:"notification_count"`
	UpdateCount       uint64                `json:"update_count"`
	DeleteCount       uint64                `json:"delete_count"`
	SyncCount         uint64                `json:"sync_count"`
	DecodeErrors      uint64                `json:"decode_errors"`
	ProtocolErrors    uint64                `json:"protocol_errors"`
	LastError         string                `json:"last_error,omitempty"`
	LastSyncAt        *time.Time            `json:"last_sync_at,omitempty"`
}

type MetricView struct {
	model.Metric
	AgeSeconds       float64  `json:"age_seconds"`
	SourceAgeSeconds *float64 `json:"source_age_seconds,omitempty"`
}

type ParentCandidateView struct {
	BNID       string    `json:"bn_id"`
	LastSeenAt time.Time `json:"last_seen_at"`
	AgeSeconds float64   `json:"age_seconds"`
	Order      uint64    `json:"order"`
}

type BNView struct {
	ID                        string                 `json:"id"`
	IdentityQuality           model.IdentityQuality  `json:"identity_quality"`
	SNMPIndex                 uint32                 `json:"snmp_index"`
	Hostname                  string                 `json:"hostname,omitempty"`
	MACAddress                string                 `json:"mac_address,omitempty"`
	FirstSeenAt               time.Time              `json:"first_seen_at"`
	LastSeenAt                time.Time              `json:"last_seen_at"`
	AgeSeconds                float64                `json:"age_seconds"`
	LastSourceTimestamp       *time.Time             `json:"last_source_timestamp,omitempty"`
	TimestampQuality          model.TimestampQuality `json:"timestamp_quality"`
	Status                    model.Status           `json:"status"`
	StatusCode                int                    `json:"status_code"`
	StatusReason              string                 `json:"status_reason"`
	ActiveStreams             int                    `json:"active_streams"`
	StreamIDs                 []string               `json:"stream_ids,omitempty"`
	ReportedActiveConnections *int64                 `json:"reported_active_connections,omitempty"`
	FreshRNCount              int                    `json:"fresh_rn_count"`
	ConnectionCountDelta      *int64                 `json:"connection_count_delta,omitempty"`
	MetricCount               int                    `json:"metric_count"`
	Metrics                   []MetricView           `json:"metrics,omitempty"`
}

type RNView struct {
	ID                   string                 `json:"id"`
	SNMPIndex            uint32                 `json:"snmp_index"`
	Hostname             string                 `json:"hostname,omitempty"`
	MACAddress           string                 `json:"mac_address,omitempty"`
	ParentBNID           string                 `json:"parent_bn_id,omitempty"`
	FirstSeenAt          time.Time              `json:"first_seen_at"`
	LastSeenAt           time.Time              `json:"last_seen_at"`
	AgeSeconds           float64                `json:"age_seconds"`
	LastSourceTimestamp  *time.Time             `json:"last_source_timestamp,omitempty"`
	TimestampQuality     model.TimestampQuality `json:"timestamp_quality"`
	Status               model.Status           `json:"status"`
	StatusCode           int                    `json:"status_code"`
	StatusReason         string                 `json:"status_reason"`
	ExplicitState        *model.ExplicitState   `json:"explicit_state,omitempty"`
	ParentCandidates     []ParentCandidateView  `json:"parent_candidates,omitempty"`
	MetricCount          int                    `json:"metric_count"`
	Metrics              []MetricView           `json:"metrics,omitempty"`
	LastDeleteAt         *time.Time             `json:"last_delete_at,omitempty"`
	LastAtomicOmissionAt *time.Time             `json:"last_atomic_omission_at,omitempty"`
}

type StatusCounts struct {
	OnlineObserved  int `json:"online_observed"`
	OfflineReported int `json:"offline_reported"`
	Stale           int `json:"stale_unconfirmed"`
	UnknownUpstream int `json:"unknown_upstream"`
	Conflict        int `json:"conflict"`
	Disconnected    int `json:"disconnected"`
	Unknown         int `json:"unknown"`
}

type Overview struct {
	GeneratedAt   time.Time `json:"generated_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	BNCount       int       `json:"bn_count"`
	RNCount       int       `json:"rn_count"`
	ActiveStreams int       `json:"active_streams"`
	Stats         Stats     `json:"stats"`
}

type Summary struct {
	GeneratedAt   time.Time    `json:"generated_at"`
	UptimeSeconds float64      `json:"uptime_seconds"`
	BNCount       int          `json:"bn_count"`
	RNCount       int          `json:"rn_count"`
	ActiveStreams int          `json:"active_streams"`
	BNStatuses    StatusCounts `json:"bn_statuses"`
	RNStatuses    StatusCounts `json:"rn_statuses"`
	Stats         Stats        `json:"stats"`
}

type Snapshot struct {
	Summary Summary         `json:"summary"`
	BNs     []BNView        `json:"bns"`
	RNs     []RNView        `json:"rns"`
	Streams []SessionRecord `json:"streams"`
}

// CurrentRecordView is one retained latest-value record with the device context
// needed by operator and monitoring views. It is not an event-history item:
// each device/path appears at most once because the Store keeps current state.
type CurrentRecordView struct {
	Kind         model.DeviceKind `json:"kind"`
	DeviceID     string           `json:"device_id"`
	Hostname     string           `json:"hostname,omitempty"`
	ParentBNID   string           `json:"parent_bn_id,omitempty"`
	Status       model.Status     `json:"status"`
	StatusCode   int              `json:"status_code"`
	StatusReason string           `json:"status_reason"`
	MetricView
}

// RecentQuery filters and bounds a newest-first scan of retained current
// values. Search is a case-insensitive substring match across device identity,
// hostname, parent BN, path, value text, stream, and subscription scope.
type RecentQuery struct {
	Limit            int
	Kind             model.DeviceKind
	DeviceID         string
	BNID             string
	PathContains     string
	Search           string
	TimestampQuality model.TimestampQuality
	Status           model.Status
	Since            time.Time
}

// RecentRecords describes the current records matched by a RecentQuery.
// Scanned is the number of latest-value paths considered, Matched is the count
// after filtering, and Items is capped by Limit.
type RecentRecords struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Scanned     int                 `json:"scanned"`
	Matched     int                 `json:"matched"`
	Returned    int                 `json:"returned"`
	Truncated   bool                `json:"truncated"`
	Items       []CurrentRecordView `json:"items"`
}

// RNSelection is a bounded fleet view intended for operator consoles. It keeps
// problem states ahead of healthy RNs without constructing a full snapshot.
type RNSelection struct {
	GeneratedAt  time.Time `json:"generated_at"`
	Total        int       `json:"total"`
	ProblemCount int       `json:"problem_count"`
	Returned     int       `json:"returned"`
	Items        []RNView  `json:"items"`
}
