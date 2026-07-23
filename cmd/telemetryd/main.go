package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"telemetryd/internal/adapter/tarana"
	"telemetryd/internal/dialout"
	"telemetryd/internal/httpapi"
	"telemetryd/internal/ingest"
	"telemetryd/internal/state"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func main() {
	defaults := state.DefaultConfig()
	grpcDefaults := dialout.DefaultConfig()
	httpDefaults := httpapi.DefaultConfig()

	var methods stringList
	var (
		grpcListen = flag.String("grpc-listen", grpcDefaults.Address, "gRPC dial-out listen address (Tarana deployments often use :80)")
		httpListen = flag.String("http-listen", httpDefaults.Address, "HTTP query API listen address")
		httpToken  = flag.String("http-token", os.Getenv("TELEMETRYD_TOKEN"), "bearer token for HTTP API; may also use TELEMETRYD_TOKEN")

		tlsCert      = flag.String("grpc-tls-cert", "", "server TLS certificate PEM")
		tlsKey       = flag.String("grpc-tls-key", "", "server TLS private key PEM")
		tlsClientCA  = flag.String("grpc-client-ca", "", "optional client certificate CA PEM")
		requireMTLS  = flag.Bool("grpc-require-client-cert", false, "require a verified gRPC client certificate")
		grpcReflect  = flag.Bool("grpc-reflection", grpcDefaults.EnableReflection, "enable gRPC reflection (service descriptors are intentionally minimal)")
		maxRecvBytes = flag.Int("grpc-max-recv-bytes", grpcDefaults.MaxRecvMessageBytes, "maximum gRPC request message size")
		maxStreams   = flag.Uint("grpc-max-streams", uint(grpcDefaults.MaxConcurrentStreams), "maximum concurrent streams per HTTP/2 connection")

		bnStale       = flag.Duration("bn-stale-after", defaults.BNStaleAfter, "BN telemetry freshness threshold")
		rnStale       = flag.Duration("rn-stale-after", defaults.RNStaleAfter, "RN telemetry freshness threshold")
		conflict      = flag.Duration("parent-conflict-window", defaults.ParentConflictWindow, "window for detecting one RN reported by multiple healthy BNs")
		futureSkew    = flag.Duration("max-future-skew", defaults.MaxFutureSkew, "maximum accepted source timestamp lead over receive time")
		maxBNs        = flag.Int("max-bns", defaults.MaxBNs, "maximum BNs retained in memory")
		maxRNs        = flag.Int("max-rns", defaults.MaxRNs, "maximum RNs retained in memory")
		maxMetrics    = flag.Int("max-metrics-per-device", defaults.MaxMetricsPerDevice, "maximum latest-value paths retained per device")
		atomicOffline = flag.Bool("atomic-omission-means-offline", false, "treat a fully omitted RN in an atomic notification as explicitly offline (normally leave false)")
		requireStream = flag.Bool("ready-requires-stream", false, "make /readyz fail until at least one stream is active")

		logLevel = flag.String("log-level", "info", "debug, info, warn, or error")
		logJSON  = flag.Bool("log-json", true, "write structured JSON logs")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Var(&methods, "grpc-method", "accepted bidi-stream RPC; repeatable (default /Nokia.SROS.DialoutTelemetry/Publish)")
	flag.Parse()

	if *showVer {
		fmt.Printf("telemetryd %s commit=%s built=%s\n", version, commit, buildDate)
		return
	}
	logger := newLogger(*logLevel, *logJSON)
	if len(methods) == 0 {
		methods = append(methods, dialout.DefaultMethod)
	}

	store := state.New(state.Config{
		BNStaleAfter:               *bnStale,
		RNStaleAfter:               *rnStale,
		ParentConflictWindow:       *conflict,
		MaxFutureSkew:              *futureSkew,
		MaxMetricsPerDevice:        *maxMetrics,
		MaxBNs:                     *maxBNs,
		MaxRNs:                     *maxRNs,
		MaxSessions:                defaults.MaxSessions,
		AtomicOmissionMeansOffline: *atomicOffline,
	}, time.Now().UTC())
	processor := ingest.New(store, tarana.New(), logger)

	grpcServer, err := dialout.New(dialout.Config{
		Address:              *grpcListen,
		Methods:              methods,
		TLSCertFile:          *tlsCert,
		TLSKeyFile:           *tlsKey,
		TLSClientCAFile:      *tlsClientCA,
		RequireClientCert:    *requireMTLS,
		MaxRecvMessageBytes:  *maxRecvBytes,
		MaxConcurrentStreams: uint32(*maxStreams),
		EnableReflection:     *grpcReflect,
	}, processor, logger)
	if err != nil {
		logger.Error("invalid gRPC configuration", "error", err)
		os.Exit(2)
	}
	httpServer, err := httpapi.New(httpapi.Config{
		Address:               *httpListen,
		BearerToken:           *httpToken,
		RequireStreamForReady: *requireStream,
	}, store, logger)
	if err != nil {
		logger.Error("invalid HTTP configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logger.Info("telemetryd starting", "version", version, "commit", commit, "build_date", buildDate, "adapter", "tarana")
	errors := make(chan error, 2)
	go func() { errors <- grpcServer.Run(ctx) }()
	go func() { errors <- httpServer.Run(ctx) }()

	first := <-errors
	if first != nil {
		logger.Error("server stopped with error", "error", first)
	}
	cancel()
	second := <-errors
	if second != nil {
		logger.Error("server stopped with error", "error", second)
	}
	if first != nil || second != nil {
		os.Exit(1)
	}
	logger.Info("telemetryd stopped")
}

func newLogger(levelText string, jsonOutput bool) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelText)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	options := &slog.HandlerOptions{Level: level}
	if jsonOutput {
		return slog.New(slog.NewJSONHandler(os.Stdout, options))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, options))
}
