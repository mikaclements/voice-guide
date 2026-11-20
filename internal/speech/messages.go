package speech

import (
	"context"
	"encoding/json"
	"errors"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/conversation"
)

// turnIn is what the browser sends on its data channel: a turn; or, as screen,
// what the product's screen shows now, sent on its own, numbered n; or warm,
// asking for the session to be readied for the user's words.
type turnIn struct {
	Type        string    `json:"type"`
	RequestID   uuid.UUID `json:"request_id"`
	Words       string    `json:"words"`
	ScreenInput []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"screen_input"`
	Opening bool `json:"opening"`
	N       int  `json:"n"`
}

// out is a message to the browser: the words heard of a turn the user speaks,
// final once it is taken with them; a piece of the reply's text; how the turn
// ended (done, lost, refused with a reason, or failed); then spoken, once its
// audio has all played, or interrupted, with the reply's text as far as the
// user heard it, when they cut in. Either of the last says, for a turn the
// user spoke, how long after their speech ended the reply's first audio went
// out. How a turn ended, done or lost, says in screen_taken the last screen
// message whose blocks went with a turn; screen_refused says screen message n
// can't be used. ready says the session can take the user's words, once per
// connection the page asked on with warm.
type out struct {
	Type         string  `json:"type"`
	RequestID    string  `json:"request_id,omitempty"`
	Text         string  `json:"text,omitempty"`
	Final        bool    `json:"final,omitempty"`
	TurnID       string  `json:"turn_id,omitempty"`
	Seq          int     `json:"seq,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	Detail       string  `json:"detail,omitempty"`
	Failed       bool    `json:"failed,omitempty"`
	Model        string  `json:"model,omitempty"`
	Heard        *string `json:"heard,omitempty"`
	FirstAudioMS int64   `json:"first_audio_ms,omitempty"`
	ScreenTaken  int     `json:"screen_taken,omitempty"`
	N            int     `json:"n,omitempty"`
}

// Why a turn was refused, so the page knows what to do with the words: send
// them again once the agent is free (busy), count them as taken (repeated),
// change them (unusable, unreadable), start a new session (ended), or, for
// an opening the session doesn't take, nothing (opening-refused).
const (
	reasonBusy           = "busy"
	reasonRepeated       = "repeated"
	reasonUnusable       = "unusable"
	reasonUnreadable     = "unreadable"
	reasonNoSession      = "no-session"
	reasonEnded          = "ended"
	reasonOpeningRefused = "opening-refused"
)

const openingRefused = "the session already has a turn, or its agent waits for the user, so the agent doesn't open it"

func requestOf(m turnIn) string {
	if m.RequestID == uuid.Nil() {
		return ""
	}
	return m.RequestID.String()
}

// ending tells the browser how the turn ended: answered, lost once its reply
// had started, refused with a reason, or failed before its reply, when it
// may have been taken, so the page sends it again with the same request ID.
func (s *Service) ending(request string, turn conversation.Turn, err error, started bool) out {
	var failed *conversation.ReplyFailedError
	var unusable *conversation.ScreenInputError
	refused := out{Type: "refused", RequestID: request}
	switch {
	case err == nil:
		return out{Type: "done", RequestID: request, TurnID: turn.ID.String(), Seq: turn.Seq, Model: turn.Model()}
	case errors.As(err, &failed):
		s.log.Warn("the model's reply failed", "kind", failed.Kind)
		return out{Type: "lost", RequestID: request, TurnID: turn.ID.String(), Seq: turn.Seq, Model: turn.Model()}
	case started:
		s.log.Error("take a turn", "err", err)
		return out{Type: "lost", RequestID: request}
	case errors.Is(err, conversation.ErrTurnInFlight):
		refused.Reason, refused.Detail = reasonBusy, "the session is answering another turn; send it again once it has finished"
	case errors.Is(err, conversation.ErrTurnRepeated):
		refused.Reason, refused.Detail = reasonRepeated, "this request_id already took a turn in this session"
	case errors.As(err, &unusable), errors.Is(err, conversation.ErrWordsUnusable),
		errors.Is(err, conversation.ErrScreenInputTooLarge), errors.Is(err, conversation.ErrWordsTooLong):
		refused.Reason, refused.Detail = reasonUnusable, err.Error()
	case errors.Is(err, conversation.ErrNoSuchSession):
		refused.Reason, refused.Detail = reasonNoSession, "no such session"
	case errors.Is(err, conversation.ErrSessionEnded):
		refused.Reason, refused.Detail = reasonEnded, "the session has ended; start a new one"
	case errors.Is(err, conversation.ErrOpeningRefused):
		refused.Reason, refused.Detail = reasonOpeningRefused, openingRefused
	case errors.Is(err, context.Canceled):
		return out{Type: "failed", RequestID: request} // the connection ended: nothing broke
	default:
		s.log.Error("take a turn", "err", err)
		return out{Type: "failed", RequestID: request}
	}
	return refused
}

// tell sends the browser a message. It fails only once the connection is
// ending, when no one is left to tell.
func (s *Service) tell(conn Connection, m out) {
	if b, err := json.Marshal(m); err == nil {
		_ = conn.Send(b)
	}
}
