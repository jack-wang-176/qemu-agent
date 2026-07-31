package runstream

import (
	"context"
)

type NopSink struct{}

func (NopSink) Emit(ctx context.Context, _ Event) error {
	return ctx.Err()
}

func NormalizeSink(s EventSink) EventSink {
	if s == nil {
		return NopSink{}
	}
	return s
}
