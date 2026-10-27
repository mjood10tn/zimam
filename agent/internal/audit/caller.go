package audit

import "context"

// Caller is who asked for an audited action: the peer's uid, the panel's
// actor and the request id (§5.8).
type Caller struct {
	PeerUID   *uint32 `json:"peer_uid"`
	Actor     string  `json:"actor"`
	RequestID string  `json:"request_id,omitempty"`
}

type callerKey struct{}

// WithCaller returns ctx carrying c, for handlers that write their own
// audit entries.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller ctx carries, or the zero Caller.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}
