package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/adapter"
	"telemetryd/internal/model"
	"telemetryd/internal/pathutil"
	"telemetryd/internal/state"
)

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Ingestor struct {
	store     *state.Store
	adapter   adapter.Adapter
	logger    *slog.Logger
	clock     Clock
	recorders []BatchRecorder
}

func New(store *state.Store, deviceAdapter adapter.Adapter, logger *slog.Logger, options ...Option) *Ingestor {
	result := &Ingestor{store: store, adapter: deviceAdapter, logger: logger, clock: realClock{}}
	for _, option := range options {
		option(result)
	}
	return result
}

func (i *Ingestor) OpenSession(meta model.SessionMeta) string {
	return i.store.OpenSession(meta, i.clock.Now())
}

func (i *Ingestor) CloseSession(sessionID, reason string) {
	i.store.CloseSession(sessionID, reason, i.clock.Now())
}

func (i *Ingestor) HandleResponse(ctx context.Context, sessionID, method string, response *gnmi.SubscribeResponse) error {
	receivedAt := i.clock.Now()
	sequence, order, session, ok := i.store.RecordMessage(sessionID, receivedAt)
	if !ok {
		return fmt.Errorf("unknown stream %q", sessionID)
	}
	if response == nil {
		i.store.RecordProtocolError(sessionID, "nil SubscribeResponse", receivedAt)
		return fmt.Errorf("nil SubscribeResponse")
	}

	if notification := response.GetUpdate(); notification != nil {
		identity := i.adapter.ResolveBN(session.Meta, notification, model.Identity{ID: session.BNID, Quality: session.BNIDQuality})
		prefix := pathutil.FromGNMI(notification.GetPrefix())
		batch := model.NotificationBatch{
			SessionID:         sessionID,
			ScopeID:           subscriptionScopeID(method, sessionID, session.Meta.Metadata),
			Method:            method,
			BN:                identity,
			ReceivedAt:        receivedAt,
			SourceTimestampNS: notification.GetTimestamp(),
			Atomic:            notification.GetAtomic(),
			Prefix:            prefix,
			MessageSequence:   sequence,
			ObservationOrder:  order,
		}

		batch.Updates = make([]model.Observation, 0, len(notification.GetUpdate()))
		for _, update := range notification.GetUpdate() {
			if update == nil {
				continue
			}
			path := pathutil.Join(notification.GetPrefix(), update.GetPath())
			value, err := DecodeValue(update.GetVal())
			if err != nil {
				i.store.RecordDecodeError(sessionID, err.Error(), receivedAt)
				i.logger.Warn("gNMI value was retained with a degraded representation", "stream_id", sessionID, "path", pathutil.Canonical(path), "error", err)
			}
			rnID, isRN := i.adapter.ResolveRN(path)
			batch.Updates = append(batch.Updates, model.Observation{
				RNID:               rnID,
				Path:               path,
				CanonicalPath:      pathutil.Canonical(path),
				BasePath:           pathutil.Base(path),
				Keys:               pathutil.FlattenKeys(path),
				Value:              value,
				ReportedDuplicates: update.GetDuplicates(),
				Hints:              i.adapter.Hints(path, value, isRN),
			})
		}

		batch.Deletes = make([]model.Deletion, 0, len(notification.GetDelete()))
		for _, deletion := range notification.GetDelete() {
			path := pathutil.Join(notification.GetPrefix(), deletion)
			rnID, _ := i.adapter.ResolveRN(path)
			batch.Deletes = append(batch.Deletes, model.Deletion{
				RNID:           rnID,
				Path:           path,
				CanonicalPath:  pathutil.Canonical(path),
				BasePath:       pathutil.Base(path),
				ConnectionRoot: i.adapter.IsConnectionRoot(path),
			})
		}

		for _, recorder := range i.recorders {
			if err := recorder.RecordBatch(ctx, batch); err != nil {
				i.logger.Error("notification recorder failed", "stream_id", sessionID, "error", err)
			}
		}
		if err := i.store.Apply(batch); err != nil {
			i.store.RecordProtocolError(sessionID, err.Error(), receivedAt)
			return err
		}
		return nil
	}

	if response.GetSyncResponse() {
		i.store.RecordSync(sessionID, receivedAt)
		return nil
	}
	if protocolError := response.GetError(); protocolError != nil {
		message := fmt.Sprintf("code=%d message=%s", protocolError.GetCode(), protocolError.GetMessage())
		i.store.RecordProtocolError(sessionID, message, receivedAt)
		return nil
	}

	i.store.RecordProtocolError(sessionID, "SubscribeResponse had no response payload", receivedAt)
	return nil
}

// subscriptionScopeID identifies the logical dial-out subscription that owns
// an atomic snapshot. A stream ID is connection-scoped and changes on
// reconnect, while subscription-name is stable when the sender supplies it.
// Falling back to the stream keeps atomic invalidation conservative when the
// sender gives us no subscription identity.
func subscriptionScopeID(method, sessionID string, metadata map[string]string) string {
	name := strings.TrimSpace(metadata["subscription-name"])
	if name == "" {
		return sessionID
	}
	return strings.TrimSpace(method) + "|" + name
}
