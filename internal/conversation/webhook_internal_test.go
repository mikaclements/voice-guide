package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"
)

func TestAHugeSessionLeavesOutItsEarliestTurnsToFitTheLimit(t *testing.T) {
	d := deliveryJSON{ID: uuid.NewV7(), Type: "session.ended", Timestamp: time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC), Data: endedSession{ID: uuid.NewV7()}}
	var turns []turnJSON
	for i := range 12 {
		turns = append(turns, turnJSON{Seq: i + 1, Words: fmt.Sprintf("Question %02d about the <lamp> & the fog?", i+1), Reply: "Bearing 040."})
	}
	all, err := deliveryBody(d, turns, 1<<20)
	if err != nil {
		t.Fatalf("deliveryBody: %v", err)
	}
	for _, limit := range []int{len(all), len(all) - 1, len(all) / 2, 200, 10} {
		body, err := deliveryBody(d, turns, limit)
		if err != nil {
			t.Fatalf("deliveryBody: %v", err)
		}
		var got endedSessionRead
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("the body doesn't read: %v", err)
		}
		kept := got.Data.Turns
		switch {
		case len(body) > limit && len(kept) > 0:
			t.Errorf("limit %d: body of %d bytes, with %d turns", limit, len(body), len(kept))
		case got.Data.TurnsLeftOut+len(kept) != len(turns):
			t.Errorf("limit %d: %d left out and %d kept, want all %d counted", limit, got.Data.TurnsLeftOut, len(kept), len(turns))
		case len(kept) > 0 && kept[len(kept)-1].Seq != len(turns):
			t.Errorf("limit %d: kept up to turn %d, want the latest kept, the earliest left out", limit, kept[len(kept)-1].Seq)
		case limit == len(all) && got.Data.TurnsLeftOut != 0:
			t.Errorf("a body exactly at the limit left out %d turns, want none", got.Data.TurnsLeftOut)
		case limit == len(all)-1 && got.Data.TurnsLeftOut != 1:
			t.Errorf("a byte over the limit left out %d turns, want the earliest one", got.Data.TurnsLeftOut)
		}
		for i := 1; i < len(kept); i++ {
			if kept[i].Seq != kept[i-1].Seq+1 {
				t.Errorf("limit %d: turns %d then %d, want them in order with none between", limit, kept[i-1].Seq, kept[i].Seq)
			}
		}
	}
}

func TestTheCapLeavesOutTurnsButKeepsTheSummaryAndItsDataWhole(t *testing.T) {
	summary := "Covered the lamp."
	data := json.RawMessage(`{"shopping_list":["lamp oil","wicks"]}`)
	d := deliveryJSON{ID: uuid.NewV7(), Type: "session.ended", Data: endedSession{ID: uuid.NewV7(), Summary: &summary, SummaryStatus: SummaryWritten, SummaryData: data}}
	body, err := deliveryBody(d, []turnJSON{{Seq: 1, Words: "Is the lamp lit?", Reply: "Bearing 040."}}, 10)
	if err != nil {
		t.Fatalf("deliveryBody: %v", err)
	}
	if !strings.Contains(string(body), `"summary":"Covered the lamp.","summary_status":"written","summary_data":{"shopping_list":["lamp oil","wicks"]}`) || !strings.Contains(string(body), `"turns_left_out":1`) {
		t.Errorf("body %s, want the turn left out and the summary and its data whole", body)
	}
}

func TestASignatureMatchesThePublishedExampleForItsScheme(t *testing.T) {
	headers, err := signed(NewWebhookSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"), "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil || headers["webhook-signature"] != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" ||
		headers["webhook-id"] != "msg_p5jXN8AQM9LWM0D4loKWxJek" || headers["webhook-timestamp"] != "1614265330" {
		t.Errorf("signed = %v, %v; want the published example's headers", headers, err)
	}
}

// endedSessionRead is what TestAHugeSessionLeavesOutItsEarliestTurnsToFitTheLimit reads back.
type endedSessionRead struct {
	Data struct {
		Turns        []turnJSON `json:"turns"`
		TurnsLeftOut int        `json:"turns_left_out"`
	} `json:"data"`
}
