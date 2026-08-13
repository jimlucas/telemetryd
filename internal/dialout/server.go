package dialout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/emptypb"
	"telemetryd/internal/model"
)

const DefaultMethod = "/Nokia.SROS.DialoutTelemetry/Publish"

type Handler interface {
	OpenSession(model.SessionMeta) string
	CloseSession(sessionID, reason string)
	HandleResponse(context.Context, string, string, *gnmi.SubscribeResponse) error
}

type Config struct {
	Address              string
	Methods              []string
	TLSCertFile          string
	TLSKeyFile           string
	TLSClientCAFile      string
	RequireClientCert    bool
	MaxRecvMessageBytes  int
	MaxConcurrentStreams uint32
	KeepaliveMinTime     time.Duration
	KeepaliveTime        time.Duration
	KeepaliveTimeout     time.Duration
	MaxConnectionIdle    time.Duration
	MaxConnectionAge     time.Duration
	MetadataKeys         []string
	EnableReflection     bool
}

func DefaultConfig() Config {
	return Config{
		Address:              ":50051",
		Methods:              []string{DefaultMethod},
		MaxRecvMessageBytes:  32 << 20,
		MaxConcurrentStreams: 1024,
		KeepaliveMinTime:     30 * time.Second,
		KeepaliveTime:        2 * time.Minute,
		KeepaliveTimeout:     20 * time.Second,
		MaxConnectionIdle:    30 * time.Minute,
		MaxConnectionAge:     24 * time.Hour,
		MetadataKeys: []string{
			"system-name",
			"subscription-name",
			"target",
			"device-id",
			"device_id",
		},
		EnableReflection: false,
	}
}

type registeredService interface {
	telemetrydDialoutService()
}

type Server struct {
	cfg      Config
	handler  Handler
	logger   *slog.Logger
	grpc     *grpc.Server
	listener net.Listener
}

func New(cfg Config, handler Handler, logger *slog.Logger) (*Server, error) {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Address) == "" {
		cfg.Address = defaults.Address
	}
	if len(cfg.Methods) == 0 {
		cfg.Methods = defaults.Methods
	}
	if cfg.MaxRecvMessageBytes <= 0 {
		cfg.MaxRecvMessageBytes = defaults.MaxRecvMessageBytes
	}
	if cfg.MaxConcurrentStreams == 0 {
		cfg.MaxConcurrentStreams = defaults.MaxConcurrentStreams
	}
	if cfg.KeepaliveMinTime <= 0 {
		cfg.KeepaliveMinTime = defaults.KeepaliveMinTime
	}
	if cfg.KeepaliveTime <= 0 {
		cfg.KeepaliveTime = defaults.KeepaliveTime
	}
	if cfg.KeepaliveTimeout <= 0 {
		cfg.KeepaliveTimeout = defaults.KeepaliveTimeout
	}
	if cfg.MaxConnectionIdle <= 0 {
		cfg.MaxConnectionIdle = defaults.MaxConnectionIdle
	}
	if cfg.MaxConnectionAge <= 0 {
		cfg.MaxConnectionAge = defaults.MaxConnectionAge
	}
	if len(cfg.MetadataKeys) == 0 {
		cfg.MetadataKeys = defaults.MetadataKeys
	}
	if handler == nil {
		return nil, errors.New("nil ingestor")
	}
	if logger == nil {
		logger = slog.Default()
	}
	normalizedMethods := make([]string, 0, len(cfg.Methods))
	seenMethods := make(map[string]struct{}, len(cfg.Methods))
	for _, configuredMethod := range cfg.Methods {
		service, method, err := splitMethod(configuredMethod)
		if err != nil {
			return nil, err
		}
		fullMethod := "/" + service + "/" + method
		if _, duplicate := seenMethods[fullMethod]; duplicate {
			return nil, fmt.Errorf("duplicate gRPC method %q", fullMethod)
		}
		seenMethods[fullMethod] = struct{}{}
		normalizedMethods = append(normalizedMethods, fullMethod)
	}
	cfg.Methods = normalizedMethods
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return nil, errors.New("both TLS certificate and key are required")
	}
	if cfg.RequireClientCert && cfg.TLSClientCAFile == "" {
		return nil, errors.New("requiring client certificates needs a client CA file")
	}
	return &Server{cfg: cfg, handler: handler, logger: logger}, nil
}

func (*Server) telemetrydDialoutService() {}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Address, err)
	}
	s.listener = listener

	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(s.cfg.MaxRecvMessageBytes),
		grpc.MaxConcurrentStreams(s.cfg.MaxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             s.cfg.KeepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:              s.cfg.KeepaliveTime,
			Timeout:           s.cfg.KeepaliveTimeout,
			MaxConnectionIdle: s.cfg.MaxConnectionIdle,
			MaxConnectionAge:  s.cfg.MaxConnectionAge,
		}),
	}
	if s.cfg.TLSCertFile != "" {
		tlsConfig, err := loadTLSConfig(s.cfg)
		if err != nil {
			_ = listener.Close()
			return err
		}
		options = append(options, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	s.grpc = grpc.NewServer(options...)

	// Register the existing Nokia-compatible dial-out service.
	for _, descriptor := range buildDescriptors(s.cfg.Methods, s.handlePublish) {
		s.grpc.RegisterService(descriptor, s)
	}

	// Register Tarana's native TNMI dial-out service.
	registerTNMI(s.grpc, s)

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s.grpc, healthServer)
	if s.cfg.EnableReflection {
		reflection.Register(s.grpc)
	}

	serveResult := make(chan error, 1)
	go func() {
		s.logger.Info("gRPC dial-out listener started", "address", listener.Addr().String(), "methods", s.cfg.Methods, "tls", s.cfg.TLSCertFile != "")
		serveResult <- s.grpc.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		stopped := make(chan struct{})
		go func() {
			s.grpc.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			s.grpc.Stop()
		}
		return nil
	case err := <-serveResult:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("serve gRPC: %w", err)
		}
		return nil
	}
}

func (s *Server) Address() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.cfg.Address
}

func (s *Server) handlePublish(method string, stream grpc.ServerStream) (resultErr error) {
	meta := sessionMeta(stream.Context(), method, s.cfg.MetadataKeys)
	sessionID := s.handler.OpenSession(meta)
	s.logger.Info("dial-out stream opened", "stream_id", sessionID, "method", method, "peer", meta.Peer, "client_subject", meta.ClientSubject)
	closeReason := "client closed stream"
	defer func() {
		if recovered := recover(); recovered != nil {
			closeReason = fmt.Sprintf("panic: %v", recovered)
			s.logger.Error("panic while processing dial-out stream", "stream_id", sessionID, "method", method, "peer", meta.Peer, "panic", recovered, "stack", string(debug.Stack()))
			resultErr = fmt.Errorf("dial-out handler panic: %v", recovered)
		}
		s.handler.CloseSession(sessionID, closeReason)
		s.logger.Info("dial-out stream closed", "stream_id", sessionID, "method", method, "peer", meta.Peer, "reason", closeReason)
	}()

	for {
		response := new(gnmi.SubscribeResponse)
		if err := stream.RecvMsg(response); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			closeReason = err.Error()
			return err
		}

		if err := s.handler.HandleResponse(stream.Context(), sessionID, method, response); err != nil {
			// A malformed observation must not tear down an otherwise useful BN
			// stream. The error remains visible in stream and process diagnostics.
			s.logger.Error("failed to reconcile dial-out message", "stream_id", sessionID, "method", method, "error", err)
		}

		// Tarana's public listener contract returns an empty PublishResponse.
		// Any empty proto message has the same zero-byte wire representation.
		if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
			closeReason = err.Error()
			return err
		}
	}
}

type publishHandler func(method string, stream grpc.ServerStream) error

func buildDescriptors(methods []string, handler publishHandler) []*grpc.ServiceDesc {
	groups := make(map[string][]string)
	for _, fullMethod := range methods {
		service, method, _ := splitMethod(fullMethod)
		groups[service] = append(groups[service], method)
	}
	services := make([]string, 0, len(groups))
	for service := range groups {
		services = append(services, service)
	}
	sort.Strings(services)

	result := make([]*grpc.ServiceDesc, 0, len(services))
	for _, service := range services {
		methodNames := groups[service]
		sort.Strings(methodNames)
		streams := make([]grpc.StreamDesc, 0, len(methodNames))
		for _, methodName := range methodNames {
			fullMethod := "/" + service + "/" + methodName
			name := methodName
			method := fullMethod
			streams = append(streams, grpc.StreamDesc{
				StreamName:    name,
				ServerStreams: true,
				ClientStreams: true,
				Handler: func(_ any, stream grpc.ServerStream) error {
					return handler(method, stream)
				},
			})
		}
		result = append(result, &grpc.ServiceDesc{
			ServiceName: service,
			HandlerType: (*registeredService)(nil),
			Streams:     streams,
			Metadata:    "dynamic-dialout.proto",
		})
	}
	return result
}

func splitMethod(value string) (string, string, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("gRPC method %q must look like /package.Service/Method", value)
	}
	return parts[0], parts[1], nil
}

func sessionMeta(ctx context.Context, method string, allowedKeys []string) model.SessionMeta {
	result := model.SessionMeta{Method: method, Metadata: make(map[string]string)}
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
	}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		for key, values := range incoming {
			key = strings.ToLower(key)
			if _, keep := allowed[key]; !keep || len(values) == 0 {
				continue
			}
			result.Metadata[key] = values[0]
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p != nil {
		if p.Addr != nil {
			result.Peer = p.Addr.String()
			result.PeerHost = peerHost(result.Peer)
		}
		if info, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(info.State.PeerCertificates) > 0 {
			result.ClientSubject = info.State.PeerCertificates[0].Subject.String()
		}
	}
	return result
}

func peerHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address, "[]")
}

func loadTLSConfig(cfg Config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load gRPC TLS key pair: %w", err)
	}
	result := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	if cfg.TLSClientCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read gRPC client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("gRPC client CA file contained no usable certificates")
		}
		result.ClientCAs = pool
		if cfg.RequireClientCert {
			result.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			result.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}
	return result, nil
}
