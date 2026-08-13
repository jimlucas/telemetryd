package dialout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	TNMIServiceName = "tnmi.DialTcc"

	TNMIIsAliveMethod = "/tnmi.DialTcc/IsAlive"

	TNMIPushSubscriptionUpdatesMethod = "/tnmi.DialTcc/PushSubscriptionUpdates"
)

// registerTNMI registers the Tarana TNMI dial-out service.
//
// Recovered Tarana service definition:
//
//	service DialTcc {
//	    rpc PushSubscriptionUpdates(stream gnmi.SubscribeResponse)
//	        returns (UpdateAck);
//
//	    rpc IsAlive(Empty)
//	        returns (Empty);
//	}
//
// Both tnmi.Empty and tnmi.UpdateAck are zero-field protobuf messages.
// emptypb.Empty therefore has the identical protobuf wire representation.
func registerTNMI(grpcServer *grpc.Server, server *Server) {
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: TNMIServiceName,
		HandlerType: (*registeredService)(nil),

		Methods: []grpc.MethodDesc{
			{
				MethodName: "IsAlive",
				Handler: func(
					_ any,
					ctx context.Context,
					dec func(any) error,
					interceptor grpc.UnaryServerInterceptor,
				) (any, error) {
					request := new(emptypb.Empty)

					if err := dec(request); err != nil {
						return nil, err
					}

					handler := func(
						ctx context.Context,
						req any,
					) (any, error) {
						server.logger.Debug(
							"TNMI IsAlive request",
							"method",
							TNMIIsAliveMethod,
						)

						return &emptypb.Empty{}, nil
					}

					if interceptor == nil {
						return handler(ctx, request)
					}

					info := &grpc.UnaryServerInfo{
						Server:     server,
						FullMethod: TNMIIsAliveMethod,
					}

					return interceptor(
						ctx,
						request,
						info,
						handler,
					)
				},
			},
		},

		Streams: []grpc.StreamDesc{
			{
				StreamName:    "PushSubscriptionUpdates",
				ClientStreams: true,
				ServerStreams: false,

				Handler: func(
					_ any,
					stream grpc.ServerStream,
				) error {
					return server.handleTNMIPushSubscriptionUpdates(
						stream,
					)
				},
			},
		},

		Metadata: "tnmi_dialout.proto",
	}, server)
}

// handleTNMIPushSubscriptionUpdates receives Tarana telemetry.
//
// Tarana sends standard gnmi.SubscribeResponse protobuf messages over
// this client-streaming RPC. Unlike Nokia Publish, Tarana does not expect
// one acknowledgement per telemetry message. It expects one UpdateAck
// when the client finishes the stream.
func (s *Server) handleTNMIPushSubscriptionUpdates(
	stream grpc.ServerStream,
) (resultErr error) {
	method := TNMIPushSubscriptionUpdatesMethod

	meta := sessionMeta(
		stream.Context(),
		method,
		s.cfg.MetadataKeys,
	)

	sessionID := s.handler.OpenSession(meta)

	s.logger.Info(
		"TNMI telemetry stream opened",
		"stream_id",
		sessionID,
		"method",
		method,
		"peer",
		meta.Peer,
		"client_subject",
		meta.ClientSubject,
	)

	closeReason := "client closed stream"

	defer func() {
		if recovered := recover(); recovered != nil {
			closeReason = fmt.Sprintf(
				"panic: %v",
				recovered,
			)

			s.logger.Error(
				"panic while processing TNMI telemetry stream",
				"stream_id",
				sessionID,
				"method",
				method,
				"peer",
				meta.Peer,
				"panic",
				recovered,
				"stack",
				string(debug.Stack()),
			)

			resultErr = fmt.Errorf(
				"TNMI telemetry handler panic: %v",
				recovered,
			)
		}

		s.handler.CloseSession(
			sessionID,
			closeReason,
		)

		s.logger.Info(
			"TNMI telemetry stream closed",
			"stream_id",
			sessionID,
			"method",
			method,
			"peer",
			meta.Peer,
			"reason",
			closeReason,
		)
	}()

	for {
		response := new(gnmi.SubscribeResponse)

		err := stream.RecvMsg(response)

		if err != nil {
			if errors.Is(err, io.EOF) {
				// Tarana's UpdateAck protobuf contains no fields.
				//
				// emptypb.Empty is therefore wire-compatible with
				// tnmi.UpdateAck.
				if err := stream.SendMsg(
					&emptypb.Empty{},
				); err != nil {
					closeReason = err.Error()
					return err
				}

				return nil
			}

			closeReason = err.Error()
			return err
		}

		if err := s.handler.HandleResponse(
			stream.Context(),
			sessionID,
			method,
			response,
		); err != nil {
			// A malformed observation should not terminate an
			// otherwise useful BN telemetry stream.
			s.logger.Error(
				"failed to reconcile TNMI telemetry message",
				"stream_id",
				sessionID,
				"method",
				method,
				"error",
				err,
			)
		}
	}
}
