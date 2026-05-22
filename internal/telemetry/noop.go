package telemetry

import "github.com/segmentio/analytics-go/v3"

type noopClient struct{}

func (noopClient) Close() error                      { return nil }
func (noopClient) Enqueue(_ analytics.Message) error { return nil }
