package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"telemetryd/internal/model"
	"telemetryd/internal/state"
)

type Config struct {
	Address               string
	BearerToken           string
	RequireStreamForReady bool
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
}

func DefaultConfig() Config {
	return Config{
		Address:           "127.0.0.1:8080",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

type Server struct {
	cfg      Config
	store    *state.Store
	logger   *slog.Logger
	http     *http.Server
	listener net.Listener
}

func New(cfg Config, store *state.Store, logger *slog.Logger) (*Server, error) {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Address) == "" {
		cfg.Address = defaults.Address
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = defaults.ReadHeaderTimeout
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaults.ReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaults.WriteTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaults.IdleTimeout
	}
	if store == nil {
		return nil, errors.New("nil state store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{cfg: cfg, store: store, logger: logger}
	server.http = &http.Server{
		Addr:              cfg.Address,
		Handler:           server.routes(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	return server, nil
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on HTTP %s: %w", s.cfg.Address, err)
	}
	s.listener = listener
	result := make(chan error, 1)
	go func() {
		s.logger.Info("HTTP query API started", "address", listener.Addr().String(), "dashboard", dashboardURL(listener.Addr()), "authentication", s.cfg.BearerToken != "")
		result <- s.http.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdown); err != nil {
			_ = s.http.Close()
		}
		return nil
	case err := <-result:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}

func (s *Server) Address() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.Address
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.root)
	mux.HandleFunc("GET /ui", s.uiRedirect)
	mux.HandleFunc("GET /ui/{asset...}", s.uiAsset)
	mux.HandleFunc("GET /favicon.svg", s.favicon)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/overview", s.overview)
	mux.HandleFunc("GET /v1/recent", s.recent)
	mux.HandleFunc("GET /v1/summary", s.summary)
	mux.HandleFunc("GET /v1/snapshot", s.snapshot)
	mux.HandleFunc("GET /v1/bns", s.bns)
	mux.HandleFunc("GET /v1/bns/{id}", s.bn)
	mux.HandleFunc("GET /v1/rns", s.rns)
	mux.HandleFunc("GET /v1/rns/{id}", s.rn)
	mux.HandleFunc("GET /v1/attention/rns", s.rnAttention)
	mux.HandleFunc("GET /v1/streams", s.streams)
	mux.HandleFunc("GET /v1/lookup", s.lookup)
	mux.HandleFunc("GET /v1/schema", s.schema)
	mux.HandleFunc("GET /zabbix/collector", s.zabbixCollector)
	mux.HandleFunc("GET /zabbix/discovery/bns", s.zabbixDiscoverBNs)
	mux.HandleFunc("GET /zabbix/discovery/rns", s.zabbixDiscoverRNs)
	mux.HandleFunc("GET /zabbix/value", s.zabbixValue)
	mux.HandleFunc("GET /metrics", s.prometheus)
	return s.accessLog(s.authenticate(mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	activeStreams := s.store.ActiveStreamCount()
	if s.cfg.RequireStreamForReady && activeStreams == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":         "not_ready",
			"active_streams": 0,
			"reason":         "no active telemetry stream",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "active_streams": activeStreams})
}

func (s *Server) overview(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Overview(time.Now().UTC()))
}

func (s *Server) summary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Summary(time.Now().UTC()))
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Snapshot(time.Now().UTC(), queryBool(r, "metrics", false)))
}

func (s *Server) bns(w http.ResponseWriter, r *http.Request) {
	items := s.store.BNs(time.Now().UTC(), queryBool(r, "metrics", false))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if statusFilter != "" || search != "" {
		filtered := items[:0]
		for _, item := range items {
			if statusFilter != "" && string(item.Status) != statusFilter {
				continue
			}
			if search != "" && !containsFold(search, item.ID, item.Hostname, item.MACAddress, string(item.Status), item.StatusReason) {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	total := len(items)
	items, offset, limit, err := pageSlice(items, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "count": len(items), "offset": offset, "limit": limit,
		"truncated": offset+len(items) < total, "items": items,
	})
}

func (s *Server) bn(w http.ResponseWriter, r *http.Request) {
	id, err := url.PathUnescape(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid BN ID")
		return
	}
	item, ok := s.store.GetBN(id, time.Now().UTC(), queryBool(r, "metrics", true))
	if !ok {
		writeError(w, http.StatusNotFound, "BN not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rns(w http.ResponseWriter, r *http.Request) {
	items := s.store.RNs(time.Now().UTC(), queryBool(r, "metrics", false))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	bnFilter := strings.TrimSpace(r.URL.Query().Get("bn"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if statusFilter != "" || bnFilter != "" || search != "" {
		filtered := items[:0]
		for _, item := range items {
			if statusFilter != "" && string(item.Status) != statusFilter {
				continue
			}
			if bnFilter != "" && item.ParentBNID != bnFilter {
				continue
			}
			if search != "" && !containsFold(search, item.ID, item.Hostname, item.MACAddress, item.ParentBNID, string(item.Status), item.StatusReason) {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	total := len(items)
	items, offset, limit, err := pageSlice(items, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "count": len(items), "offset": offset, "limit": limit,
		"truncated": offset+len(items) < total, "items": items,
	})
}

func (s *Server) rn(w http.ResponseWriter, r *http.Request) {
	id, err := url.PathUnescape(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid RN ID")
		return
	}
	item, ok := s.store.GetRN(id, time.Now().UTC(), queryBool(r, "metrics", true))
	if !ok {
		writeError(w, http.StatusNotFound, "RN not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) streams(w http.ResponseWriter, _ *http.Request) {
	items := s.store.Sessions()
	writeJSON(w, http.StatusOK, map[string]any{"count": len(items), "items": items})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	kind, id, path, ok := lookupArguments(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "kind, id, and path are required")
		return
	}
	metric, found := s.store.LookupWithKeys(kind, id, path, queryKeySelectors(r))
	if !found {
		writeError(w, http.StatusNotFound, "metric not found or base path is ambiguous")
		return
	}
	now := time.Now().UTC()
	view := state.MetricView{
		Metric:     metric,
		AgeSeconds: nonNegativeSeconds(now.Sub(metric.ReceivedAt)),
	}
	if metric.SourceTimestamp != nil {
		age := nonNegativeSeconds(now.Sub(*metric.SourceTimestamp))
		view.SourceAgeSeconds = &age
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) schema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"adapter": "tarana",
		"status_codes": map[string]int{
			"offline_reported_or_disconnected": 0,
			"online_observed":                  1,
			"stale_unconfirmed":                2,
			"unknown_upstream":                 3,
			"conflict":                         4,
			"unknown":                          5,
		},
		"timestamp_qualities": []model.TimestampQuality{
			model.TimestampSource,
			model.TimestampMissing,
			model.TimestampInvalidPast,
			model.TimestampFuture,
			model.TimestampRegressed,
		},
		"current_records": map[string]any{
			"endpoint":  "/v1/recent?limit=200&kind=rn&since=10m&q=snr",
			"semantics": "newest-first latest retained value per device and canonical path; not event history",
		},
		"dashboard": "/ui/",
		"operator_views": map[string]string{
			"rn_attention": "/v1/attention/rns?limit=40",
		},
		"zabbix": map[string]any{
			"discovery": []string{"/zabbix/discovery/bns", "/zabbix/discovery/rns"},
			"value":     "/zabbix/value?kind=rn&id=<RN>&field=status_code",
			"metric":    "/zabbix/value?kind=rn&id=<RN>&path=/connections/connection/state/dl-snr&attribute=value_text",
		},
	})
}

func (s *Server) zabbixCollector(w http.ResponseWriter, r *http.Request) {
	field := strings.TrimSpace(r.URL.Query().Get("field"))
	if field == "" {
		field = "active_streams"
	}
	value, ok := collectorField(s.store.Overview(time.Now().UTC()), field)
	if !ok {
		writePlainError(w, http.StatusBadRequest, "unsupported collector field")
		return
	}
	writePlain(w, value)
}

func (s *Server) zabbixDiscoverBNs(w http.ResponseWriter, _ *http.Request) {
	bns := s.store.BNs(time.Now().UTC(), false)
	data := make([]map[string]string, 0, len(bns))
	for _, bn := range bns {
		data = append(data, map[string]string{
			"{#BNID}":       bn.ID,
			"{#BNHOSTNAME}": bn.Hostname,
			"{#BNSTATUS}":   string(bn.Status),
			"{#BNMAC}":      bn.MACAddress,
			"{#SNMPINDEX}":  strconv.FormatUint(uint64(bn.SNMPIndex), 10),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) zabbixDiscoverRNs(w http.ResponseWriter, r *http.Request) {
	rns := s.store.RNs(time.Now().UTC(), false)
	bnFilter := strings.TrimSpace(r.URL.Query().Get("bn"))
	data := make([]map[string]string, 0, len(rns))
	for _, rn := range rns {
		if bnFilter != "" && rn.ParentBNID != bnFilter {
			continue
		}
		data = append(data, map[string]string{
			"{#RNID}":       rn.ID,
			"{#RNHOSTNAME}": rn.Hostname,
			"{#BNID}":       rn.ParentBNID,
			"{#RNSTATUS}":   string(rn.Status),
			"{#RNMAC}":      rn.MACAddress,
			"{#SNMPINDEX}":  strconv.FormatUint(uint64(rn.SNMPIndex), 10),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) zabbixValue(w http.ResponseWriter, r *http.Request) {
	kindText := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" || (kindText != string(model.KindBN) && kindText != string(model.KindRN)) {
		writePlainError(w, http.StatusBadRequest, "kind must be bn or rn, and id is required")
		return
	}
	kind := model.DeviceKind(kindText)
	if path := strings.TrimSpace(r.URL.Query().Get("path")); path != "" {
		metric, ok := s.store.LookupWithKeys(kind, id, path, queryKeySelectors(r))
		if !ok {
			writePlainError(w, http.StatusNotFound, "metric not found or base path is ambiguous")
			return
		}
		value, ok := metricAttribute(metric, strings.TrimSpace(r.URL.Query().Get("attribute")), time.Now().UTC())
		if !ok {
			writePlainError(w, http.StatusBadRequest, "unsupported metric attribute")
			return
		}
		writePlain(w, value)
		return
	}

	field := strings.TrimSpace(r.URL.Query().Get("field"))
	if field == "" {
		field = "status_code"
	}
	var value string
	var ok bool
	if kind == model.KindBN {
		device, found := s.store.GetBN(id, time.Now().UTC(), false)
		if !found {
			writePlainError(w, http.StatusNotFound, "BN not found")
			return
		}
		value, ok = bnField(device, field)
	} else {
		device, found := s.store.GetRN(id, time.Now().UTC(), false)
		if !found {
			writePlainError(w, http.StatusNotFound, "RN not found")
			return
		}
		value, ok = rnField(device, field)
	}
	if !ok {
		writePlainError(w, http.StatusBadRequest, "unsupported field")
		return
	}
	writePlain(w, value)
}

func (s *Server) prometheus(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot(time.Now().UTC(), false)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	lines := []string{
		"# HELP telemetryd_active_streams Current active dial-out streams.",
		"# TYPE telemetryd_active_streams gauge",
		fmt.Sprintf("telemetryd_active_streams %d", snapshot.Summary.ActiveStreams),
		"# HELP telemetryd_messages_total gNMI SubscribeResponse messages received.",
		"# TYPE telemetryd_messages_total counter",
		fmt.Sprintf("telemetryd_messages_total %d", snapshot.Summary.Stats.Messages),
		"# HELP telemetryd_updates_total gNMI updates received.",
		"# TYPE telemetryd_updates_total counter",
		fmt.Sprintf("telemetryd_updates_total %d", snapshot.Summary.Stats.Updates),
		"# HELP telemetryd_timestamp_missing_total Notifications or observations without a source timestamp.",
		"# TYPE telemetryd_timestamp_missing_total counter",
		fmt.Sprintf("telemetryd_timestamp_missing_total %d", snapshot.Summary.Stats.MissingSourceTimestamp),
		"# HELP telemetryd_source_regressions_total Latest-received values whose source timestamp moved backward.",
		"# TYPE telemetryd_source_regressions_total counter",
		fmt.Sprintf("telemetryd_source_regressions_total %d", snapshot.Summary.Stats.SourceRegressions),
		"# HELP telemetryd_exact_duplicates_total Repeated values with the same nonzero source timestamp.",
		"# TYPE telemetryd_exact_duplicates_total counter",
		fmt.Sprintf("telemetryd_exact_duplicates_total %d", snapshot.Summary.Stats.ExactDuplicates),
		"# HELP telemetryd_reported_duplicates_total Duplicates coalesced and reported by the sender in Update.duplicates.",
		"# TYPE telemetryd_reported_duplicates_total counter",
		fmt.Sprintf("telemetryd_reported_duplicates_total %d", snapshot.Summary.Stats.ReportedDuplicates),
		"# HELP telemetryd_bn_status Reconciled BN status code.",
		"# TYPE telemetryd_bn_status gauge",
	}
	for _, bn := range snapshot.BNs {
		lines = append(lines, fmt.Sprintf("telemetryd_bn_status{bn_id=%s,hostname=%s,status=%s} %d",
			promQuote(bn.ID), promQuote(bn.Hostname), promQuote(string(bn.Status)), bn.StatusCode))
		lines = append(lines, fmt.Sprintf("telemetryd_bn_last_received_age_seconds{bn_id=%s} %s",
			promQuote(bn.ID), formatFloat(bn.AgeSeconds)))
	}
	lines = append(lines,
		"# HELP telemetryd_rn_status Reconciled RN status code.",
		"# TYPE telemetryd_rn_status gauge",
	)
	for _, rn := range snapshot.RNs {
		lines = append(lines, fmt.Sprintf("telemetryd_rn_status{rn_id=%s,bn_id=%s,hostname=%s,status=%s} %d",
			promQuote(rn.ID), promQuote(rn.ParentBNID), promQuote(rn.Hostname), promQuote(string(rn.Status)), rn.StatusCode))
		lines = append(lines, fmt.Sprintf("telemetryd_rn_last_received_age_seconds{rn_id=%s,bn_id=%s} %s",
			promQuote(rn.ID), promQuote(rn.ParentBNID), formatFloat(rn.AgeSeconds)))
	}
	_, _ = fmt.Fprintln(w, strings.Join(lines, "\n"))
}

func queryKeySelectors(r *http.Request) map[string]string {
	selectors := make(map[string]string)
	for key, values := range r.URL.Query() {
		if !strings.HasPrefix(key, "key.") || len(values) == 0 {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, "key."))
		if name == "" {
			continue
		}
		selectors[name] = values[0]
	}
	if len(selectors) == 0 {
		return nil
	}
	return selectors
}

func lookupArguments(r *http.Request) (model.DeviceKind, string, string, bool) {
	kind := model.DeviceKind(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind"))))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	ok := (kind == model.KindBN || kind == model.KindRN) && id != "" && path != ""
	return kind, id, path, ok
}

func collectorField(overview state.Overview, field string) (string, bool) {
	stats := overview.Stats
	switch field {
	case "uptime_seconds":
		return formatFloat(overview.UptimeSeconds), true
	case "bn_count":
		return strconv.Itoa(overview.BNCount), true
	case "rn_count":
		return strconv.Itoa(overview.RNCount), true
	case "active_streams":
		return strconv.Itoa(overview.ActiveStreams), true
	case "messages":
		return strconv.FormatUint(stats.Messages, 10), true
	case "notifications":
		return strconv.FormatUint(stats.Notifications, 10), true
	case "updates":
		return strconv.FormatUint(stats.Updates, 10), true
	case "deletes":
		return strconv.FormatUint(stats.Deletes, 10), true
	case "atomic_notifications":
		return strconv.FormatUint(stats.AtomicNotifications, 10), true
	case "atomic_removals":
		return strconv.FormatUint(stats.AtomicRemovals, 10), true
	case "deleted_metrics":
		return strconv.FormatUint(stats.DeletedMetrics, 10), true
	case "exact_duplicates":
		return strconv.FormatUint(stats.ExactDuplicates, 10), true
	case "repeated_values":
		return strconv.FormatUint(stats.RepeatedValues, 10), true
	case "reported_duplicates":
		return strconv.FormatUint(stats.ReportedDuplicates, 10), true
	case "value_changes":
		return strconv.FormatUint(stats.ValueChanges, 10), true
	case "source_regressions":
		return strconv.FormatUint(stats.SourceRegressions, 10), true
	case "missing_source_timestamp":
		return strconv.FormatUint(stats.MissingSourceTimestamp, 10), true
	case "invalid_source_timestamp":
		return strconv.FormatUint(stats.InvalidSourceTimestamp, 10), true
	case "decode_errors":
		return strconv.FormatUint(stats.DecodeErrors, 10), true
	case "protocol_errors":
		return strconv.FormatUint(stats.ProtocolErrors, 10), true
	case "rejected_devices":
		return strconv.FormatUint(stats.RejectedDevices, 10), true
	case "identity_merges":
		return strconv.FormatUint(stats.IdentityMerges, 10), true
	case "evicted_metrics":
		return strconv.FormatUint(stats.EvictedMetrics, 10), true
	case "opened_streams":
		return strconv.FormatUint(stats.OpenedStreams, 10), true
	case "closed_streams":
		return strconv.FormatUint(stats.ClosedStreams, 10), true
	case "sync_responses":
		return strconv.FormatUint(stats.SyncResponses, 10), true
	default:
		return "", false
	}
}

func metricAttribute(metric model.Metric, attribute string, now time.Time) (string, bool) {
	if attribute == "" {
		attribute = "value_text"
	}
	switch attribute {
	case "value", "value_text":
		return metric.ValueText, true
	case "value_type":
		return metric.ValueType, true
	case "received_unix":
		return strconv.FormatInt(metric.ReceivedAt.Unix(), 10), true
	case "received_unix_ms":
		return strconv.FormatInt(metric.ReceivedAt.UnixMilli(), 10), true
	case "source_timestamp_unix":
		if metric.SourceTimestamp == nil {
			return "0", true
		}
		return strconv.FormatInt(metric.SourceTimestamp.Unix(), 10), true
	case "source_timestamp_ns":
		return strconv.FormatInt(metric.SourceTimestampNS, 10), true
	case "age_seconds":
		return formatFloat(nonNegativeSeconds(now.Sub(metric.ReceivedAt))), true
	case "source_age_seconds":
		if metric.SourceTimestamp == nil {
			return "-1", true
		}
		return formatFloat(nonNegativeSeconds(now.Sub(*metric.SourceTimestamp))), true
	case "timestamp_quality":
		return string(metric.TimestampQuality), true
	case "samples":
		return strconv.FormatUint(metric.Samples, 10), true
	case "value_changes":
		return strconv.FormatUint(metric.ValueChanges, 10), true
	case "exact_duplicates":
		return strconv.FormatUint(metric.ExactDuplicates, 10), true
	case "repeated_values":
		return strconv.FormatUint(metric.RepeatedValues, 10), true
	case "source_regressions":
		return strconv.FormatUint(metric.SourceRegressions, 10), true
	case "reported_duplicates":
		return strconv.FormatUint(metric.ReportedDuplicates, 10), true
	case "stream_id":
		return metric.StreamID, true
	case "scope_id":
		return metric.ScopeID, true
	case "path":
		return metric.Path, true
	case "base_path":
		return metric.BasePath, true
	case "first_seen_unix":
		return strconv.FormatInt(metric.FirstSeenAt.Unix(), 10), true
	case "changed_unix":
		return strconv.FormatInt(metric.ChangedAt.Unix(), 10), true
	case "message_sequence":
		return strconv.FormatUint(metric.MessageSequence, 10), true
	case "observation_order":
		return strconv.FormatUint(metric.ObservationOrder, 10), true
	case "source_bn_id":
		return metric.SourceBNID, true
	default:
		return "", false
	}
}

func bnField(device state.BNView, field string) (string, bool) {
	common, ok := commonDeviceField(device.Status, device.StatusCode, device.StatusReason, device.AgeSeconds, device.FirstSeenAt, device.LastSeenAt, device.LastSourceTimestamp, device.TimestampQuality, device.Hostname, device.MACAddress, device.SNMPIndex, device.MetricCount, field)
	if ok {
		return common, true
	}
	switch field {
	case "identity_quality":
		return string(device.IdentityQuality), true
	case "active_streams":
		return strconv.Itoa(device.ActiveStreams), true
	case "fresh_rn_count":
		return strconv.Itoa(device.FreshRNCount), true
	case "reported_active_connections":
		if device.ReportedActiveConnections == nil {
			return "-1", true
		}
		return strconv.FormatInt(*device.ReportedActiveConnections, 10), true
	case "connection_count_delta":
		if device.ConnectionCountDelta == nil {
			return "0", true
		}
		return strconv.FormatInt(*device.ConnectionCountDelta, 10), true
	default:
		return "", false
	}
}

func rnField(device state.RNView, field string) (string, bool) {
	common, ok := commonDeviceField(device.Status, device.StatusCode, device.StatusReason, device.AgeSeconds, device.FirstSeenAt, device.LastSeenAt, device.LastSourceTimestamp, device.TimestampQuality, device.Hostname, device.MACAddress, device.SNMPIndex, device.MetricCount, field)
	if ok {
		return common, true
	}
	switch field {
	case "parent_bn_id":
		return device.ParentBNID, true
	case "explicit_state":
		if device.ExplicitState == nil {
			return "", true
		}
		return device.ExplicitState.State, true
	case "last_delete_unix":
		if device.LastDeleteAt == nil {
			return "0", true
		}
		return strconv.FormatInt(device.LastDeleteAt.Unix(), 10), true
	case "last_atomic_omission_unix":
		if device.LastAtomicOmissionAt == nil {
			return "0", true
		}
		return strconv.FormatInt(device.LastAtomicOmissionAt.Unix(), 10), true
	case "parent_conflict_count":
		return strconv.Itoa(len(device.ParentCandidates)), true
	default:
		return "", false
	}
}

func commonDeviceField(status model.Status, statusCode int, statusReason string, age float64, firstSeen, lastSeen time.Time, source *time.Time, quality model.TimestampQuality, hostname, mac string, snmpIndex uint32, metricCount int, field string) (string, bool) {
	switch field {
	case "status":
		return string(status), true
	case "status_code":
		return strconv.Itoa(statusCode), true
	case "status_reason":
		return statusReason, true
	case "available":
		return boolNumber(status == model.StatusOnlineObserved), true
	case "known_offline":
		return boolNumber(status == model.StatusOfflineReported || status == model.StatusDisconnected), true
	case "age_seconds":
		return formatFloat(age), true
	case "first_seen_unix":
		return strconv.FormatInt(firstSeen.Unix(), 10), true
	case "last_seen_unix":
		return strconv.FormatInt(lastSeen.Unix(), 10), true
	case "last_source_timestamp_unix":
		if source == nil {
			return "0", true
		}
		return strconv.FormatInt(source.Unix(), 10), true
	case "source_timestamp_present":
		return boolNumber(source != nil), true
	case "timestamp_quality":
		return string(quality), true
	case "hostname":
		return hostname, true
	case "mac_address":
		return mac, true
	case "snmp_index":
		return strconv.FormatUint(uint64(snmpIndex), 10), true
	case "metric_count":
		return strconv.Itoa(metricCount), true
	default:
		return "", false
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.cfg.BearerToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if provided == "" {
			provided = r.Header.Get("X-Telemetry-Token")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.BearerToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="telemetryd"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		tracked := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(tracked, r)
		s.logger.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "status", tracked.status, "bytes", tracked.bytes, "duration", time.Since(started))
	})
}

type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(value []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(value)
	w.bytes += written
	return written, err
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "status": status})
}

func writePlain(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintln(w, value)
}

func writePlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, message)
}

func queryBool(r *http.Request, name string, defaultValue bool) bool {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func boolNumber(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func nonNegativeSeconds(value time.Duration) float64 {
	if value < 0 {
		return 0
	}
	return value.Seconds()
}

func promQuote(value string) string {
	return strconv.Quote(value)
}
