package ingest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/model"
)

func DecodeValue(value *gnmi.TypedValue) (model.DecodedValue, error) {
	if value == nil || value.GetValue() == nil {
		return model.DecodedValue{Type: "null", Data: nil, Text: "null"}, nil
	}

	switch typed := value.GetValue().(type) {
	case *gnmi.TypedValue_StringVal:
		return scalar("string", typed.StringVal), nil
	case *gnmi.TypedValue_IntVal:
		return model.DecodedValue{Type: "int64", Data: typed.IntVal, Text: strconv.FormatInt(typed.IntVal, 10)}, nil
	case *gnmi.TypedValue_UintVal:
		return model.DecodedValue{Type: "uint64", Data: typed.UintVal, Text: strconv.FormatUint(typed.UintVal, 10)}, nil
	case *gnmi.TypedValue_BoolVal:
		return model.DecodedValue{Type: "bool", Data: typed.BoolVal, Text: strconv.FormatBool(typed.BoolVal)}, nil
	case *gnmi.TypedValue_BytesVal:
		encoded := base64.StdEncoding.EncodeToString(typed.BytesVal)
		return model.DecodedValue{Type: "bytes_base64", Data: encoded, Text: encoded}, nil
	case *gnmi.TypedValue_FloatVal:
		asFloat := float64(typed.FloatVal)
		text := strconv.FormatFloat(asFloat, 'g', -1, 32)
		if !finiteFloat(asFloat) {
			return model.DecodedValue{Type: "float32_non_finite", Data: text, Text: text}, fmt.Errorf("non-finite float32 value %s", text)
		}
		return model.DecodedValue{Type: "float32", Data: asFloat, Text: text}, nil
	case *gnmi.TypedValue_DoubleVal:
		text := strconv.FormatFloat(typed.DoubleVal, 'g', -1, 64)
		if !finiteFloat(typed.DoubleVal) {
			return model.DecodedValue{Type: "float64_non_finite", Data: text, Text: text}, fmt.Errorf("non-finite float64 value %s", text)
		}
		return model.DecodedValue{Type: "float64", Data: typed.DoubleVal, Text: text}, nil
	case *gnmi.TypedValue_DecimalVal:
		text := decimalText(typed.DecimalVal)
		return model.DecodedValue{Type: "decimal64", Data: text, Text: text}, nil
	case *gnmi.TypedValue_LeaflistVal:
		values := make([]any, 0, len(typed.LeaflistVal.GetElement()))
		texts := make([]string, 0, len(typed.LeaflistVal.GetElement()))
		for _, element := range typed.LeaflistVal.GetElement() {
			decoded, err := DecodeValue(element)
			if err != nil {
				return model.DecodedValue{}, err
			}
			values = append(values, decoded.Data)
			texts = append(texts, decoded.Text)
		}
		text, err := json.Marshal(values)
		if err != nil {
			fallback := "[" + strings.Join(texts, ",") + "]"
			return model.DecodedValue{Type: "leaflist_degraded", Data: values, Text: fallback}, fmt.Errorf("normalize leaf-list: %w", err)
		}
		return model.DecodedValue{Type: "leaflist", Data: values, Text: string(text)}, nil
	case *gnmi.TypedValue_AnyVal:
		if typed.AnyVal == nil {
			return model.DecodedValue{Type: "any", Data: nil, Text: "null"}, nil
		}
		data := map[string]any{
			"type_url":     typed.AnyVal.GetTypeUrl(),
			"value_base64": base64.StdEncoding.EncodeToString(typed.AnyVal.GetValue()),
		}
		text, _ := json.Marshal(data)
		return model.DecodedValue{Type: "any", Data: data, Text: string(text)}, nil
	case *gnmi.TypedValue_JsonVal:
		return decodeJSON("json", typed.JsonVal)
	case *gnmi.TypedValue_JsonIetfVal:
		return decodeJSON("json_ietf", typed.JsonIetfVal)
	case *gnmi.TypedValue_AsciiVal:
		return scalar("ascii", typed.AsciiVal), nil
	case *gnmi.TypedValue_ProtoBytes:
		encoded := base64.StdEncoding.EncodeToString(typed.ProtoBytes)
		return model.DecodedValue{Type: "proto_bytes_base64", Data: encoded, Text: encoded}, nil
	default:
		return model.DecodedValue{}, fmt.Errorf("unsupported gNMI TypedValue %T", typed)
	}
}

func scalar(kind, value string) model.DecodedValue {
	return model.DecodedValue{Type: kind, Data: value, Text: value}
}

func decodeJSON(kind string, raw []byte) (model.DecodedValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		// Preserve malformed payloads instead of throwing away the observation.
		text := string(raw)
		return model.DecodedValue{Type: kind + "_invalid", Data: text, Text: text}, fmt.Errorf("decode %s value: %w", kind, err)
	}
	compact, err := json.Marshal(decoded)
	if err != nil {
		return model.DecodedValue{}, fmt.Errorf("normalize %s value: %w", kind, err)
	}
	return model.DecodedValue{Type: kind, Data: decoded, Text: string(compact)}, nil
}

func decimalText(value *gnmi.Decimal64) string {
	if value == nil {
		return "0"
	}
	digits := value.GetDigits()
	precision := int(value.GetPrecision())
	if precision <= 0 {
		return strconv.FormatInt(digits, 10)
	}

	negative := digits < 0
	var magnitude uint64
	if negative {
		// This form also handles math.MinInt64 without overflowing.
		magnitude = uint64(-(digits + 1)) + 1
	} else {
		magnitude = uint64(digits)
	}
	text := strconv.FormatUint(magnitude, 10)
	if len(text) <= precision {
		text = strings.Repeat("0", precision-len(text)+1) + text
	}
	cut := len(text) - precision
	text = text[:cut] + "." + text[cut:]
	if negative && magnitude != 0 {
		text = "-" + text
	}
	return text
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
