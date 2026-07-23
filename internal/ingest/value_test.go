package ingest

import (
	"testing"

	"github.com/openconfig/gnmi/proto/gnmi"
)

func TestDecodeDecimal(t *testing.T) {
	value, err := DecodeValue(&gnmi.TypedValue{Value: &gnmi.TypedValue_DecimalVal{DecimalVal: &gnmi.Decimal64{Digits: -1234, Precision: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if value.Text != "-12.34" {
		t.Fatalf("decimal = %q", value.Text)
	}
}

func TestDecodeJSONNormalizes(t *testing.T) {
	value, err := DecodeValue(&gnmi.TypedValue{Value: &gnmi.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`{ "b": 2, "a": 1 }`)}})
	if err != nil {
		t.Fatal(err)
	}
	if value.Text != `{"a":1,"b":2}` {
		t.Fatalf("json = %q", value.Text)
	}
}
