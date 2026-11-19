package conversation

import (
	"context"
	"strings"
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

// modelReply is the model's reply as readReply read it.
type modelReply struct {
	text string
	// firstText is when the first text arrived; zero when none did.
	firstText time.Time
	// report is the model service's report on a reply it finished, nil when it
	// sent none.
	report *ModelReport
	// failed is a *ReplyFailedError naming why the model didn't finish, or nil.
	failed error
	// cutIn says the user cut in before the model let go of the reply, which
	// may still be running.
	cutIn bool
	// unfinished says the user cut in before the model's final event.
	unfinished bool
}

// readReply reads the model's reply from events, handing each piece of text to
// send as it arrives, until the model lets go of the channel or the user cuts
// in by closing interrupt, which a nil interrupt never does. After a cut-in the
// model may still hold the channel: the caller stops the reply at once, and
// waits for the model to let go before it returns.
func (s *Service) readReply(events <-chan ModelEvent, interrupt <-chan struct{}, send func(text string)) modelReply {
	var text strings.Builder
	var firstText time.Time
	var final ModelEvent // the model's last word, done or failed; the zero event until it comes
	for {
		select {
		case e, open := <-events:
			// The interrupt is checked again before any text goes out: with an
			// event and the interrupt both ready, select picks either.
			cutIn := closed(interrupt)
			switch {
			case !open:
			case isFinal(e):
				final = e
			case !cutIn:
				if firstText.IsZero() {
					firstText = s.now()
				}
				text.WriteString(e.Text)
				send(e.Text)
			}
			if cutIn || !open {
				return replyOf(text.String(), firstText, final, cutIn)
			}
		case <-interrupt:
			// A final event already waiting when the interrupt won still counts,
			// so a reply that failed as the user cut in stays failed.
			select {
			case e, open := <-events:
				if open && isFinal(e) {
					final = e
				}
			default:
			}
			return replyOf(text.String(), firstText, final, true)
		}
	}
}

// isFinal says whether e is the model's last word on its reply: done, or failed.
func isFinal(e ModelEvent) bool { return e.Done || e.Error != "" }

// replyOf says how a reply ended: as the model's final event says, if it sent
// one; otherwise where the user cut in, if they did; otherwise the model let go
// of it unfinished, which counts as unavailable.
func replyOf(text string, firstText time.Time, final ModelEvent, cutIn bool) modelReply {
	r := modelReply{text: text, firstText: firstText, cutIn: cutIn}
	switch {
	case final.Done:
		r.report = final.Report
	case final.Error != "":
		r.failed = &ReplyFailedError{Kind: final.Error}
	case cutIn:
		r.unfinished = true // the reply ends where the user cut in, not by failing
	default:
		r.failed = &ReplyFailedError{Kind: ModelUnavailable}
	}
	return r
}
