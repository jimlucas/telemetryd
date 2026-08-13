package ingest

import (
	"math"
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

func TestDecodeFloatNaNAsUnavailable(t *testing.T) {
	value, err := DecodeValue(&gnmi.TypedValue{
		Value: &gnmi.TypedValue_FloatVal{
			FloatVal: float32(math.NaN()),
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if value.Type != "float32_nan" {
		t.Fatalf("type = %q, want float32_nan", value.Type)
	}

	if value.Data != nil {
		t.Fatalf("data = %#v, want nil", value.Data)
	}

	if value.Text != "" {
		t.Fatalf("text = %q, want empty", value.Text)
	}
}

func TestDecodeDoubleNaNAsUnavailable(t *testing.T) {
	value, err := DecodeValue(&gnmi.TypedValue{
		Value: &gnmi.TypedValue_DoubleVal{
			DoubleVal: math.NaN(),
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if value.Type != "float64_nan" {
		t.Fatalf("type = %q, want float64_nan", value.Type)
	}

	if value.Data != nil {
		t.Fatalf("data = %#v, want nil", value.Data)
	}
}
