package tarana

import (
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/model"
	"telemetryd/internal/pathutil"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "tarana" }

func (a *Adapter) ResolveBN(meta model.SessionMeta, notification *gnmi.Notification, current model.Identity) model.Identity {
	if notification != nil && notification.GetPrefix() != nil {
		if target := strings.TrimSpace(notification.GetPrefix().GetTarget()); target != "" {
			return model.Identity{ID: target, Quality: model.IdentityTarget}
		}
	}
	for _, key := range []string{"system-name", "target", "device-id", "device_id"} {
		if value := strings.TrimSpace(meta.Metadata[key]); value != "" {
			return model.Identity{ID: value, Quality: model.IdentityMetadata}
		}
	}
	if current.ID != "" {
		return current
	}
	if host := strings.TrimSpace(meta.PeerHost); host != "" {
		return model.Identity{ID: host, Quality: model.IdentityPeer}
	}
	if host, _, err := net.SplitHostPort(meta.Peer); err == nil && host != "" {
		return model.Identity{ID: host, Quality: model.IdentityPeer}
	}
	return model.Identity{ID: strings.TrimSpace(meta.Peer), Quality: model.IdentityPeer}
}

func (a *Adapter) ResolveRN(path model.Path) (string, bool) {
	// Tarana's connection list is keyed by the RN device identifier. The
	// flattened Influx representation calls this connection_device-id; on the
	// wire it is normally the device-id key of the connection PathElem.
	for _, elem := range path.Elements {
		if pathutil.NormalizeName(elem.Name) != "connection" {
			continue
		}
		for _, wanted := range []string{"connectiondeviceid", "deviceid", "rnid", "serialnumber", "id", "name"} {
			for key, value := range elem.Keys {
				if pathutil.NormalizeName(key) == wanted && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value), true
				}
			}
		}
	}
	for _, elem := range path.Elements {
		for key, value := range elem.Keys {
			if pathutil.NormalizeName(key) == "connectiondeviceid" && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), true
			}
		}
	}
	return "", false
}

func (a *Adapter) Hints(path model.Path, value model.DecodedValue, isRN bool) model.ObservationHints {
	hints := model.ObservationHints{}
	if isRN {
		if pathutil.EndsWith(path, "connections", "connection", "system", "state", "hostname") {
			hints.Hostname = scalarString(value)
		}
		if pathutil.EndsWith(path, "connections", "connection", "platform", "state", "mac-address") {
			hints.MACAddress = scalarString(value)
		}
		if pathutil.ContainsSequence(path, "connections", "connection", "state") && isOperationalStateLeaf(path) {
			if state, ok := normalizeOperationalState(value); ok {
				hints.ExplicitState = state
				hints.ExplicitReason = pathutil.Base(path) + "=" + value.Text
			}
		}
		return hints
	}

	if pathutil.EndsWith(path, "system", "state", "hostname") {
		hints.Hostname = scalarString(value)
	}
	if pathutil.EndsWith(path, "platform", "components", "component", "state", "mac-address") {
		hints.MACAddress = scalarString(value)
	}
	if pathutil.EndsWith(path, "connections", "global", "state", "active-connections") {
		if parsed, ok := scalarInt64(value); ok {
			hints.ActiveConnections = &parsed
		}
	}
	return hints
}

func (a *Adapter) IsConnectionRoot(path model.Path) bool {
	if len(path.Elements) < 2 {
		return false
	}
	last := len(path.Elements) - 1
	return pathutil.NormalizeName(path.Elements[last].Name) == "connection" &&
		pathutil.NormalizeName(path.Elements[last-1].Name) == "connections"
}

func isOperationalStateLeaf(path model.Path) bool {
	// Be deliberately conservative: only a direct leaf below the keyed
	// connection/state container may determine whole-RN availability. A nested
	// radio/component status must not accidentally mark the RN down.
	elements := path.Elements
	if len(elements) < 4 {
		return false
	}
	stateIndex := len(elements) - 2
	if pathutil.NormalizeName(elements[stateIndex].Name) != "state" ||
		pathutil.NormalizeName(elements[stateIndex-1].Name) != "connection" {
		return false
	}
	leaf := pathutil.NormalizeName(elements[len(elements)-1].Name)
	switch leaf {
	case "connected", "connectionstate", "operstatus", "operationalstatus", "status", "linkstate", "associationstate":
		return true
	default:
		return false
	}
}

func normalizeOperationalState(value model.DecodedValue) (string, bool) {
	switch typed := value.Data.(type) {
	case bool:
		if typed {
			return "online", true
		}
		return "offline", true
	case int64:
		if typed == 0 {
			return "offline", true
		}
		if typed == 1 {
			return "online", true
		}
		return "", false
	case uint64:
		if typed == 0 {
			return "offline", true
		}
		if typed == 1 {
			return "online", true
		}
		return "", false
	case float64:
		if typed == 0 {
			return "offline", true
		}
		if typed == 1 {
			return "online", true
		}
		return "", false
	}

	normalized := pathutil.NormalizeName(scalarString(value))
	switch normalized {
	case "up", "online", "connected", "active", "enabled", "associated", "registered", "true", "1":
		return "online", true
	case "down", "offline", "disconnected", "inactive", "disabled", "unassociated", "deregistered", "false", "0":
		return "offline", true
	default:
		return "", false
	}
}

func scalarString(value model.DecodedValue) string {
	switch typed := value.Data.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(value.Text)
	}
}

func scalarInt64(value model.DecodedValue) (int64, bool) {
	switch typed := value.Data.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed), true
		}
		return 0, false
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed ||
			typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value.Text), 10, 64)
		return parsed, err == nil
	}
}
