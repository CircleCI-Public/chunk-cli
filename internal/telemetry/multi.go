package telemetry

import (
	"errors"

	"github.com/segmentio/analytics-go/v3"
)

// multiDestination fans a single Enqueue/Close out to every added destination.
type multiDestination struct {
	delegates []destination
}

func (mc *multiDestination) Close() error {
	errs := make([]error, 0, len(mc.delegates))
	for _, d := range mc.delegates {
		errs = append(errs, d.Close())
	}
	return errors.Join(errs...)
}

func (mc *multiDestination) Enqueue(m analytics.Track) error {
	errs := make([]error, 0, len(mc.delegates))
	for _, d := range mc.delegates {
		errs = append(errs, d.Enqueue(m))
	}
	return errors.Join(errs...)
}

func (mc *multiDestination) Add(d destination) {
	mc.delegates = append(mc.delegates, d)
}
