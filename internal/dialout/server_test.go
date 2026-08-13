package dialout

import (
	"context"
	"log/slog"
	"testing"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"telemetryd/internal/model"
)

func TestSplitMethod(t *testing.T) {
	service, method, err := splitMethod(DefaultMethod)
	if err != nil {
		t.Fatal(err)
	}
	if service != "Nokia.SROS.DialoutTelemetry" || method != "Publish" {
		t.Fatalf("split = %q/%q", service, method)
	}
	for _, invalid := range []string{"", "Publish", "/too/many/parts", "/service/"} {
		if _, _, err := splitMethod(invalid); err == nil {
			t.Fatalf("splitMethod(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestBuildDescriptorsGroupsMethods(t *testing.T) {
	descriptors := buildDescriptors([]string{
		"/Nokia.SROS.DialoutTelemetry/Publish",
		"/Example.Telemetry/Publish",
		"/Example.Telemetry/Publish2",
	}, func(string, grpc.ServerStream) error { return nil })
	if len(descriptors) != 2 {
		t.Fatalf("descriptor count = %d", len(descriptors))
	}
	if descriptors[0].ServiceName != "Example.Telemetry" || len(descriptors[0].Streams) != 2 {
		t.Fatalf("first descriptor = %#v", descriptors[0])
	}
	if descriptors[1].ServiceName != "Nokia.SROS.DialoutTelemetry" || len(descriptors[1].Streams) != 1 {
		t.Fatalf("second descriptor = %#v", descriptors[1])
	}
	if !descriptors[1].Streams[0].ClientStreams || !descriptors[1].Streams[0].ServerStreams {
		t.Fatal("Publish must be bidirectional streaming")
	}
}

func TestNewRejectsIncompleteTLSAndBadMethod(t *testing.T) {
	handler := &testHandler{}
	if _, err := New(Config{Methods: []string{"bad"}}, handler, slog.Default()); err == nil {
		t.Fatal("bad method accepted")
	}
	if _, err := New(Config{TLSCertFile: "cert.pem"}, handler, slog.Default()); err == nil {
		t.Fatal("incomplete TLS configuration accepted")
	}
	if _, err := New(Config{RequireClientCert: true}, handler, slog.Default()); err == nil {
		t.Fatal("mTLS without a CA accepted")
	}
	if _, err := New(Config{Methods: []string{DefaultMethod, "Nokia.SROS.DialoutTelemetry/Publish"}}, handler, slog.Default()); err == nil {
		t.Fatal("duplicate normalized method accepted")
	}
}

type testHandler struct{}

func (*testHandler) OpenSession(model.SessionMeta) string { return "test" }
func (*testHandler) CloseSession(string, string)          {}
func (*testHandler) HandleResponse(context.Context, string, string, *gnmi.SubscribeResponse) error {
	return nil
}
func TestNewNormalizesNativeTNMIMethods(t *testing.T) {
	handler := &testHandler{}

	server, err := New(Config{
		Methods: []string{
			DefaultMethod,
			TNMIPushSubscriptionUpdatesMethod,
			TNMIIsAliveMethod,
		},
	}, handler, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	if len(server.cfg.Methods) != 1 {
		t.Fatalf("method count = %d, want 1", len(server.cfg.Methods))
	}

	if server.cfg.Methods[0] != DefaultMethod {
		t.Fatalf(
			"method = %q, want %q",
			server.cfg.Methods[0],
			DefaultMethod,
		)
	}
}

func TestNewRejectsUnknownNativeTNMIMethod(t *testing.T) {
	handler := &testHandler{}

	if _, err := New(Config{
		Methods: []string{
			"/tnmi.DialTcc/UnknownMethod",
		},
	}, handler, slog.Default()); err == nil {
		t.Fatal("unknown tnmi.DialTcc method unexpectedly accepted")
	}
}
