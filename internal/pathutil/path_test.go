package pathutil

import (
	"testing"

	"github.com/openconfig/gnmi/proto/gnmi"
)

func TestJoinAndCanonical(t *testing.T) {
	prefix := &gnmi.Path{
		Target: "BN123",
		Elem: []*gnmi.PathElem{
			{Name: "connections"},
			{Name: "connection", Key: map[string]string{"device-id": "RN9"}},
		},
	}
	update := &gnmi.Path{Elem: []*gnmi.PathElem{{Name: "state"}, {Name: "dl-snr"}}}
	path := Join(prefix, update)
	if got, want := Canonical(path), `/connections/connection[device-id="RN9"]/state/dl-snr`; got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
	if got, want := Base(path), "/connections/connection/state/dl-snr"; got != want {
		t.Fatalf("base path = %q, want %q", got, want)
	}
	if path.Target != "BN123" {
		t.Fatalf("target = %q", path.Target)
	}
}

func TestUnderHonorsPrefixKeys(t *testing.T) {
	a := Join(nil, &gnmi.Path{Elem: []*gnmi.PathElem{
		{Name: "connection", Key: map[string]string{"device-id": "RN1"}},
		{Name: "state"},
	}})
	b := Join(nil, &gnmi.Path{Elem: []*gnmi.PathElem{
		{Name: "connection", Key: map[string]string{"device-id": "RN2"}},
	}})
	if Under(a.Elements, b.Elements) {
		t.Fatal("different list keys matched")
	}
}
