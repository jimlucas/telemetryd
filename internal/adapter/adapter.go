package adapter

import (
	"github.com/openconfig/gnmi/proto/gnmi"
	"telemetryd/internal/model"
)

type Adapter interface {
	Name() string
	ResolveBN(meta model.SessionMeta, notification *gnmi.Notification, current model.Identity) model.Identity
	ResolveRN(path model.Path) (string, bool)
	Hints(path model.Path, value model.DecodedValue, isRN bool) model.ObservationHints
	IsConnectionRoot(path model.Path) bool
}
