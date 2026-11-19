// Package smartturn asks the speech-to-text service's end-of-turn check, which runs Smart Turn:
// speech's end-of-turn adapter. It speaks plain HTTP, so it needs nothing but the standard library.
package smartturn

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mikaclements/voice-guide/internal/speech"
)

// DefaultDeadline bounds one check unless set otherwise. Checks took 79 to 142 ms over HTTP across
// every .claude/skills/measure/check-stt.sh run on an M3 Pro, so a second is about seven times
// the slowest, room for a busy machine; measured again with spoken turns. It only matters when the maximum wait for the user is
// longer than the pause before the check plus a second; otherwise the maximum wait decides first.
const DefaultDeadline = time.Second

// turnSamples is how many 16 kHz samples the check reads.
const turnSamples = int(speech.TurnAudio / time.Second * 16000)

// EndOfTurn asks the check at its address.
type EndOfTurn struct {
	url      string
	deadline time.Duration
	client   *http.Client
}

var _ speech.EndOfTurn = (*EndOfTurn)(nil)

// New returns the check at baseURL, an http or https address, each check failing after deadline,
// or DefaultDeadline when it is zero.
func New(baseURL string, deadline time.Duration) (*EndOfTurn, error) {
	if u, err := url.Parse(baseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http or https address", baseURL)
	}
	if deadline == 0 {
		deadline = DefaultDeadline
	}
	return &EndOfTurn{url: strings.TrimSuffix(baseURL, "/") + "/v1/turn/check", deadline: deadline, client: &http.Client{}}, nil
}

func (e *EndOfTurn) Finished(ctx context.Context, audio []int16) (bool, error) {
	if len(audio) == 0 {
		// Nothing heard is nothing finished, and not worth asking about.
		return false, nil
	}
	audio = audio[max(0, len(audio)-turnSamples):]
	// audio/L16 (RFC 2586): 16-bit samples in network byte order.
	body := make([]byte, 2*len(audio))
	for i, s := range audio {
		binary.BigEndian.PutUint16(body[2*i:], uint16(s))
	}
	ctx, cancel := context.WithTimeout(ctx, e.deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("end-of-turn check: %w", err)
	}
	req.Header.Set("Content-Type", "audio/L16;rate=16000")
	resp, err := e.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("end-of-turn check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The status only: a body is the service's to word, and errors reach logs.
		return false, fmt.Errorf("end-of-turn check: status %d", resp.StatusCode)
	}
	var answer struct {
		Finished bool `json:"finished"`
	}
	// An answer is a few dozen bytes; more is not an answer.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&answer); err != nil {
		return false, fmt.Errorf("end-of-turn check: read the answer: %w", err)
	}
	return answer.Finished, nil
}
