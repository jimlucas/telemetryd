package ingest

import (
	"context"

	"telemetryd/internal/model"
)

// BatchRecorder is the persistence seam. The in-memory reconciler does not
// require one, but a future WAL, message bus, or database writer can consume
// the same normalized notification batches without changing the gRPC server.
// Implementations must treat the batch as immutable, should buffer internally,
// and should return promptly. Recorder failures are observable but do not stop
// in-memory reconciliation or tear down a BN stream.
type BatchRecorder interface {
	RecordBatch(context.Context, model.NotificationBatch) error
}

type Option func(*Ingestor)

func WithRecorder(recorder BatchRecorder) Option {
	return func(ingestor *Ingestor) {
		if recorder != nil {
			ingestor.recorders = append(ingestor.recorders, recorder)
		}
	}
}
