package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"telemetryd/internal/dialout"
)

func main() {
	server := flag.String("server", "127.0.0.1:50051", "telemetryd gRPC address")
	method := flag.String("method", dialout.DefaultMethod, "dial-out RPC method")
	bnID := flag.String("bn", "BN-DEMO-001", "simulated BN ID")
	rnText := flag.String("rns", "RN-DEMO-001,RN-DEMO-002", "comma-separated RN IDs")
	interval := flag.Duration("interval", 5*time.Second, "submission interval")
	missingEvery := flag.Int("missing-timestamp-every", 0, "set source timestamp to zero every N submissions")
	regressEvery := flag.Int("regress-timestamp-every", 0, "send a source timestamp 10 minutes old every N submissions")
	duplicateEvery := flag.Int("duplicate-every", 0, "resend an identical notification every N submissions")
	tlsEnabled := flag.Bool("tls", false, "connect with TLS")
	tlsSkipVerify := flag.Bool("tls-skip-verify", false, "skip server certificate verification (test only)")
	flag.Parse()

	rns := splitList(*rnText)
	if len(rns) == 0 {
		log.Fatal("at least one RN is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx = metadata.AppendToOutgoingContext(ctx,
		"system-name", *bnID,
		"subscription-name", "telemetryd-simulator",
	)

	options := []grpc.DialOption{grpc.WithBlock()}
	if *tlsEnabled {
		options = append(options, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: *tlsSkipVerify, //nolint:gosec // explicit simulator flag
		})))
	} else {
		options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	dialContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	connection, err := grpc.DialContext(dialContext, *server, options...)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer connection.Close()

	descriptor := &grpc.StreamDesc{StreamName: methodName(*method), ServerStreams: true, ClientStreams: true}
	stream, err := connection.NewStream(ctx, descriptor, *method)
	if err != nil {
		log.Fatalf("open stream: %v", err)
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	iteration := 0
	for {
		iteration++
		notification := makeNotification(*bnID, rns, iteration, timestamp(iteration, *missingEvery, *regressEvery))
		response := &gnmi.SubscribeResponse{Response: &gnmi.SubscribeResponse_Update{Update: notification}}
		if err := sendAndAck(stream, response); err != nil {
			log.Fatalf("submission %d: %v", iteration, err)
		}
		log.Printf("submission=%d bn=%s rns=%d source_timestamp=%d", iteration, *bnID, len(rns), notification.Timestamp)

		if *duplicateEvery > 0 && iteration%*duplicateEvery == 0 {
			if err := sendAndAck(stream, response); err != nil {
				log.Fatalf("duplicate submission %d: %v", iteration, err)
			}
			log.Printf("submission=%d duplicate=true", iteration)
		}

		select {
		case <-ctx.Done():
			_ = stream.CloseSend()
			return
		case <-ticker.C:
		}
	}
}

func makeNotification(bnID string, rns []string, iteration int, timestamp int64) *gnmi.Notification {
	updates := []*gnmi.Update{
		update(path("system", "state", "hostname"), stringValue(strings.ToLower(bnID))),
		update(path("connections", "global", "state", "active-connections"), uintValue(uint64(len(rns)))),
	}
	for index, rnID := range rns {
		base := []*gnmi.PathElem{
			{Name: "connections"},
			{Name: "connection", Key: map[string]string{"device-id": rnID}},
		}
		updates = append(updates,
			update(appendPath(base, "system", "state", "hostname"), stringValue(strings.ToLower(rnID))),
			update(appendPath(base, "state", "dl-snr"), doubleValue(25+float64(index)+rand.Float64()*2)),
			update(appendPath(base, "state", "ul-snr"), doubleValue(22+float64(index)+rand.Float64()*2)),
			update(appendPath(base, "state", "path-loss"), doubleValue(105+float64(index)+rand.Float64()*2)),
			update(appendPath(base, "state", "rf-range"), doubleValue(1000+float64(index*100))),
			update(appendPath(base, "state", "connected"), boolValue(true)),
		)
		for radio := 0; radio < 2; radio++ {
			radioBase := append([]*gnmi.PathElem{}, base...)
			radioBase = append(radioBase,
				&gnmi.PathElem{Name: "radios"},
				&gnmi.PathElem{Name: "radio", Key: map[string]string{"radio-id": fmt.Sprintf("%d", radio)}},
			)
			updates = append(updates, update(
				appendPath(radioBase, "state", "rx-signal-level", "avg"),
				doubleValue(-50-float64(index*2+radio)-rand.Float64()*4),
			))
		}
	}
	return &gnmi.Notification{
		Timestamp: timestamp,
		Prefix:    &gnmi.Path{Target: bnID},
		Update:    updates,
	}
}

func sendAndAck(stream grpc.ClientStream, response *gnmi.SubscribeResponse) error {
	if err := stream.SendMsg(response); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	ack := new(emptypb.Empty)
	if err := stream.RecvMsg(ack); err != nil {
		return fmt.Errorf("receive ack: %w", err)
	}
	return nil
}

func timestamp(iteration, missingEvery, regressEvery int) int64 {
	if missingEvery > 0 && iteration%missingEvery == 0 {
		return 0
	}
	value := time.Now().UTC()
	if regressEvery > 0 && iteration%regressEvery == 0 {
		value = value.Add(-10 * time.Minute)
	}
	return value.UnixNano()
}

func update(path *gnmi.Path, value *gnmi.TypedValue) *gnmi.Update {
	return &gnmi.Update{Path: path, Val: value}
}

func path(names ...string) *gnmi.Path {
	elements := make([]*gnmi.PathElem, 0, len(names))
	for _, name := range names {
		elements = append(elements, &gnmi.PathElem{Name: name})
	}
	return &gnmi.Path{Elem: elements}
}

func appendPath(prefix []*gnmi.PathElem, names ...string) *gnmi.Path {
	elements := make([]*gnmi.PathElem, 0, len(prefix)+len(names))
	for _, element := range prefix {
		copy := &gnmi.PathElem{Name: element.Name, Key: make(map[string]string, len(element.Key))}
		for key, value := range element.Key {
			copy.Key[key] = value
		}
		elements = append(elements, copy)
	}
	for _, name := range names {
		elements = append(elements, &gnmi.PathElem{Name: name})
	}
	return &gnmi.Path{Elem: elements}
}

func stringValue(value string) *gnmi.TypedValue {
	return &gnmi.TypedValue{Value: &gnmi.TypedValue_StringVal{StringVal: value}}
}

func uintValue(value uint64) *gnmi.TypedValue {
	return &gnmi.TypedValue{Value: &gnmi.TypedValue_UintVal{UintVal: value}}
}

func doubleValue(value float64) *gnmi.TypedValue {
	return &gnmi.TypedValue{Value: &gnmi.TypedValue_DoubleVal{DoubleVal: value}}
}

func boolValue(value bool) *gnmi.TypedValue {
	return &gnmi.TypedValue{Value: &gnmi.TypedValue_BoolVal{BoolVal: value}}
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func methodName(full string) string {
	parts := strings.Split(strings.TrimPrefix(full, "/"), "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return "Publish"
}
