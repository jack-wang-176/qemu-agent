package pipelineapi

import "context"

type eventPublisherContextKey struct{}

// WithEventPublisher binds a notification publisher to one request context.
// Runtime factories may read it while constructing per-invocation ports.
func WithEventPublisher(ctx context.Context, publisher EventPublisher) context.Context {
	if publisher == nil {
		publisher = NopEventPublisher{}
	}
	return context.WithValue(ctx, eventPublisherContextKey{}, publisher)
}

// EventPublisherFromContext returns a request publisher or a non-nil no-op.
func EventPublisherFromContext(ctx context.Context) EventPublisher {
	if ctx != nil {
		if publisher, ok := ctx.Value(eventPublisherContextKey{}).(EventPublisher); ok && publisher != nil {
			return publisher
		}
	}
	return NopEventPublisher{}
}

// NopEventPublisher disables event delivery without making the runtime port nil.
type NopEventPublisher struct{}

func (NopEventPublisher) Publish(context.Context, Event) error { return nil }

var _ EventPublisher = NopEventPublisher{}
