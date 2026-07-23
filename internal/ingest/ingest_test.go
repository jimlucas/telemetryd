package ingest

import "testing"

func TestSubscriptionScopeIDUsesStableSubscriptionName(t *testing.T) {
	got := subscriptionScopeID(
		"/Nokia.SROS.DialoutTelemetry/Publish",
		"stream-1",
		map[string]string{"subscription-name": " tarana-primary "},
	)
	want := "/Nokia.SROS.DialoutTelemetry/Publish|tarana-primary"
	if got != want {
		t.Fatalf("scope = %q, want %q", got, want)
	}
}

func TestSubscriptionScopeIDFallsBackToConnection(t *testing.T) {
	if got := subscriptionScopeID("/service/Publish", "stream-1", nil); got != "stream-1" {
		t.Fatalf("scope = %q, want connection-scoped fallback", got)
	}
}
