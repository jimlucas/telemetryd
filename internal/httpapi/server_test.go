package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"telemetryd/internal/model"
	"telemetryd/internal/pathutil"
	"telemetryd/internal/state"
)

func TestZabbixValueSupportsPathKeySelectors(t *testing.T) {
	store, now := populatedStore(t)
	server, err := New(Config{}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	query := url.Values{
		"kind":         {"rn"},
		"id":           {"RN-1"},
		"path":         {"/connections/connection/radios/radio/state/rx-signal-level/avg"},
		"key.radio_id": {"1"},
	}
	request := httptest.NewRequest(http.MethodGet, "/zabbix/value?"+query.Encode(), nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := strings.TrimSpace(response.Body.String()); got != "-62" {
		t.Fatalf("value = %q", got)
	}

	request = httptest.NewRequest(http.MethodGet, "/zabbix/value?kind=rn&id=RN-1&field=status_code", nil)
	response = httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "1" {
		t.Fatalf("status value: code=%d body=%q at %s", response.Code, response.Body.String(), now)
	}
}

func TestZabbixCollectorField(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/zabbix/collector?field=rn_count", nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "1" {
		t.Fatalf("collector field: code=%d body=%q", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/zabbix/collector?field=does_not_exist", nil)
	response = httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown collector field status = %d", response.Code)
	}
}

func TestZabbixDiscoveryAndAuthentication(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{BearerToken: "secret"}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.routes()

	request := httptest.NewRequest(http.MethodGet, "/zabbix/discovery/rns", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/zabbix/discovery/rns", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0]["{#RNID}"] != "RN-1" {
		t.Fatalf("discovery payload = %#v", payload.Data)
	}

	request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health endpoint should be auth-exempt: %d", response.Code)
	}
}

func populatedStore(t *testing.T) (*state.Store, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	cfg := state.DefaultConfig()
	cfg.BNStaleAfter = time.Hour
	cfg.RNStaleAfter = time.Hour
	store := state.New(cfg, now)
	stream := store.OpenSession(model.SessionMeta{PeerHost: "10.0.0.1"}, now)

	updates := make([]model.Observation, 0, 2)
	for radio, value := range []float64{-55, -62} {
		path := model.Path{Elements: []model.PathElement{
			{Name: "connections"},
			{Name: "connection", Keys: map[string]string{"device-id": "RN-1"}},
			{Name: "radios"},
			{Name: "radio", Keys: map[string]string{"radio-id": strconv.Itoa(radio)}},
			{Name: "state"},
			{Name: "rx-signal-level"},
			{Name: "avg"},
		}}
		updates = append(updates, model.Observation{
			RNID:          "RN-1",
			Path:          path,
			CanonicalPath: pathutil.Canonical(path),
			BasePath:      pathutil.Base(path),
			Keys:          pathutil.FlattenKeys(path),
			Value: model.DecodedValue{
				Type: "float64",
				Data: value,
				Text: strconv.FormatFloat(value, 'g', -1, 64),
			},
		})
	}
	sequence, order, _, ok := store.RecordMessage(stream, now)
	if !ok {
		t.Fatal("record message")
	}
	if err := store.Apply(model.NotificationBatch{
		SessionID:         stream,
		BN:                model.Identity{ID: "BN-1", Quality: model.IdentityTarget},
		ReceivedAt:        now,
		SourceTimestampNS: now.UnixNano(),
		MessageSequence:   sequence,
		ObservationOrder:  order,
		Updates:           updates,
	}); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func TestDashboardAssetsArePublicWhileAPIStaysProtected(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{BearerToken: "secret"}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.routes()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/ui/" {
		t.Fatalf("root redirect: status=%d location=%q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/ui/", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Recently captured records") {
		t.Fatalf("dashboard: status=%d body=%q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("dashboard CSP = %q", got)
	}

	request = httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "refreshAll") {
		t.Fatalf("dashboard JavaScript: status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/summary", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status = %d", response.Code)
	}
}

func TestRecentEndpointFiltersAndValidates(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{BearerToken: "secret"}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.routes()

	request := httptest.NewRequest(http.MethodGet, "/v1/recent?kind=rn&limit=1&q=signal", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("recent status=%d body=%s", response.Code, response.Body.String())
	}
	var payload state.RecentRecords
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Matched != 2 || payload.Returned != 1 || !payload.Truncated {
		t.Fatalf("recent payload = %#v", payload)
	}
	if payload.Items[0].Kind != model.KindRN || payload.Items[0].DeviceID != "RN-1" {
		t.Fatalf("recent item = %#v", payload.Items[0])
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/recent?quality=made-up", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid quality status = %d", response.Code)
	}
}

func TestDeviceListPagination(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/rns?limit=1&offset=0&q=RN-1", nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("pagination status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Total int            `json:"total"`
		Count int            `json:"count"`
		Items []state.RNView `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || payload.Count != 1 || len(payload.Items) != 1 {
		t.Fatalf("pagination payload = %#v", payload)
	}
}

func TestRNAttentionEndpointIsBounded(t *testing.T) {
	store, _ := populatedStore(t)
	server, err := New(Config{}, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/attention/rns?limit=1", nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("attention status=%d body=%s", response.Code, response.Body.String())
	}
	var payload state.RNSelection
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || payload.Returned != 1 || len(payload.Items) != 1 {
		t.Fatalf("attention payload = %#v", payload)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/attention/rns?limit=501", nil)
	response = httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized attention limit status = %d", response.Code)
	}
}
