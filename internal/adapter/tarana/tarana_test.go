package tarana

import (
	"testing"

	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/model"
)

func TestResolveBNPrefersTargetThenMetadataThenPeer(t *testing.T) {
	adapter := New()
	meta := model.SessionMeta{
		PeerHost: "192.0.2.10",
		Metadata: map[string]string{"system-name": "BN-META"},
	}
	got := adapter.ResolveBN(meta, &gnmi.Notification{Prefix: &gnmi.Path{Target: "BN-TARGET"}}, model.Identity{})
	if got.ID != "BN-TARGET" || got.Quality != model.IdentityTarget {
		t.Fatalf("target identity = %#v", got)
	}
	got = adapter.ResolveBN(meta, &gnmi.Notification{}, model.Identity{})
	if got.ID != "BN-META" || got.Quality != model.IdentityMetadata {
		t.Fatalf("metadata identity = %#v", got)
	}
	got = adapter.ResolveBN(model.SessionMeta{PeerHost: "192.0.2.10"}, &gnmi.Notification{}, model.Identity{})
	if got.ID != "192.0.2.10" || got.Quality != model.IdentityPeer {
		t.Fatalf("peer identity = %#v", got)
	}
}

func TestResolveRNAndTaranaHints(t *testing.T) {
	adapter := New()
	path := model.Path{Elements: []model.PathElement{
		{Name: "connections"},
		{Name: "connection", Keys: map[string]string{"device-id": "RN-123"}},
		{Name: "system"},
		{Name: "state"},
		{Name: "hostname"},
	}}
	id, ok := adapter.ResolveRN(path)
	if !ok || id != "RN-123" {
		t.Fatalf("ResolveRN = %q, %v", id, ok)
	}
	hints := adapter.Hints(path, model.DecodedValue{Type: "string", Data: "rn-edge-1", Text: "rn-edge-1"}, true)
	if hints.Hostname != "rn-edge-1" {
		t.Fatalf("hostname hint = %q", hints.Hostname)
	}

	statePath := model.Path{Elements: []model.PathElement{
		{Name: "connections"},
		{Name: "connection", Keys: map[string]string{"connection_device-id": "RN-123"}},
		{Name: "state"},
		{Name: "connected"},
	}}
	hints = adapter.Hints(statePath, model.DecodedValue{Type: "bool", Data: false, Text: "false"}, true)
	if hints.ExplicitState != "offline" {
		t.Fatalf("explicit state hint = %#v", hints)
	}

	nestedStatePath := model.Path{Elements: []model.PathElement{
		{Name: "connections"},
		{Name: "connection", Keys: map[string]string{"device-id": "RN-123"}},
		{Name: "state"},
		{Name: "radio"},
		{Name: "status"},
	}}
	hints = adapter.Hints(nestedStatePath, model.DecodedValue{Type: "uint64", Data: uint64(0), Text: "0"}, true)
	if hints.ExplicitState != "" {
		t.Fatalf("nested component state was treated as whole-RN state: %#v", hints)
	}

	activePath := model.Path{Elements: []model.PathElement{
		{Name: "connections"}, {Name: "global"}, {Name: "state"}, {Name: "active-connections"},
	}}
	hints = adapter.Hints(activePath, model.DecodedValue{Type: "uint64", Data: uint64(7), Text: "7"}, false)
	if hints.ActiveConnections == nil || *hints.ActiveConnections != 7 {
		t.Fatalf("active connection hint = %#v", hints.ActiveConnections)
	}
}

func TestConnectionRoot(t *testing.T) {
	adapter := New()
	root := model.Path{Elements: []model.PathElement{
		{Name: "connections"},
		{Name: "connection", Keys: map[string]string{"device-id": "RN-1"}},
	}}
	if !adapter.IsConnectionRoot(root) {
		t.Fatal("connection root was not recognized")
	}
	root.Elements = append(root.Elements, model.PathElement{Name: "state"})
	if adapter.IsConnectionRoot(root) {
		t.Fatal("connection child was incorrectly treated as a root")
	}
}
