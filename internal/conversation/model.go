package conversation

import (
	"context"
	"time"
	"uuid"
)

// Model is the model service, which answers turns.
type Model interface {
	// Reply asks for the reply to r. Its events arrive on the channel, closed
	// after the final one, or soon after cancelling ctx stops the reply.
	Reply(ctx context.Context, r ModelRequest) <-chan ModelEvent
}

// ModelRequest asks for one reply: which model, which session, which prompt.
// Session is the nil UUID for an ask outside any session, such as a summary
// written once a session ends, for which the model service keeps nothing.
type ModelRequest struct {
	Model   string
	Session uuid.UUID
	Prompt
}

// ModelEvent is a piece of a reply's text, or the final event: done, with
// the model service's report on the reply (nil when it didn't read), or an
// error naming its kind.
type ModelEvent struct {
	Text   string
	Done   bool
	Report *ModelReport
	Error  ModelErrorKind
}

// ModelReport is what the model service says about a reply it finished: who
// answered, the tokens used (nil when the provider reported none), and its own
// times to the first text (nil when no text came) and to the end.
type ModelReport struct {
	Provider, Model string
	Usage           *ModelUsage
	FirstText       *time.Duration
	Total           time.Duration
}

// ModelUsage counts a reply's tokens: input read fresh, input read from the
// cache, input written to the cache, and output.
type ModelUsage struct {
	Input, CachedInput, CacheWrite, Output int
}

// ModelErrorKind says why a reply failed, from the model service's closed set,
// or busy: the model service itself had no free place for it.
type ModelErrorKind string

// The model service's closed set of reasons a reply failed, as it names them.
const (
	ModelRateLimited ModelErrorKind = "rate_limited"
	ModelQuota       ModelErrorKind = "quota"
	ModelOverloaded  ModelErrorKind = "overloaded"
	ModelUnavailable ModelErrorKind = "unavailable"
	ModelAuth        ModelErrorKind = "auth"
	ModelBadRequest  ModelErrorKind = "bad_request"
	ModelTooLarge    ModelErrorKind = "too_large"
	ModelUnknown     ModelErrorKind = "unknown"
)

// ModelBusy says the model service itself had no free place for the reply, at
// its own cap: nothing failed, and moments may clear it.
const ModelBusy ModelErrorKind = "busy"

// ReplyFailedError reports a reply the model didn't finish, naming why.
type ReplyFailedError struct{ Kind ModelErrorKind }

func (e *ReplyFailedError) Error() string { return "the model's reply failed: " + string(e.Kind) }
