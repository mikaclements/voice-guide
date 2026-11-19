package usercontext_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/usercontext"
	"github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

// captain is another agent of the workspace.
var captain = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b2")

// ended is when the summaries' sessions ended.
var ended = time.Date(2026, 11, 18, 9, 20, 0, 0, time.UTC)

func TestASessionsSummaryIsSavedOnceAndLoadedOnlyByTheAgentThatWroteIt(t *testing.T) {
	svc := usercontext.NewService(fake.New())
	session := uuid.NewV7()
	first, err := svc.SaveSummary(context.Background(), usercontext.SummaryRequest{Workspace: workspace, User: sailor, Agent: keeper, Session: session, SessionEnded: ended, Content: "Covered knots; tides were weak."})
	if err != nil || first.Kind != usercontext.KindSessionSummary || first.Agent != keeper || first.Session != session {
		t.Fatalf("SaveSummary = %+v, %v; want the summary saved with its agent and session", first, err)
	}
	again, err := svc.SaveSummary(context.Background(), usercontext.SummaryRequest{Workspace: workspace, User: sailor, Agent: keeper, Session: session, SessionEnded: ended, Content: "A second try."})
	if err != nil || again.ID != first.ID || again.Content != first.Content {
		t.Errorf("SaveSummary again = %+v, %v; want the first, saved once", again, err)
	}
	if _, err := svc.SaveSummary(context.Background(), usercontext.SummaryRequest{Workspace: workspace, User: sailor, Agent: captain, Session: uuid.NewV7(), SessionEnded: ended, Content: "Covered the crossing."}); err != nil {
		t.Fatalf("SaveSummary for the captain: %v", err)
	}

	for agent, want := range map[uuid.UUID][]string{keeper: {"Covered knots; tides were weak."}, captain: {"Covered the crossing."}} {
		got, err := svc.Load(context.Background(), usercontext.LoadRequest{Workspace: workspace, User: sailor, Agent: agent, Kinds: []string{usercontext.KindSessionSummary}, MaxChars: 1000})
		if err != nil || !slices.Equal(contents(got.Items), want) {
			t.Errorf("agent %s loaded %q, %v; want only its own summaries, %q", agent, contents(got.Items), err, want)
		}
	}
}

func TestASessionsSummaryReadsBackForItsSessionOnly(t *testing.T) {
	svc := usercontext.NewService(fake.New())
	session := uuid.NewV7()
	if _, found, err := svc.SessionSummary(context.Background(), workspace, sailor, session); found || err != nil {
		t.Fatalf("SessionSummary before any = %v, %v; want none", found, err)
	}
	saved, err := svc.SaveSummary(context.Background(), usercontext.SummaryRequest{Workspace: workspace, User: sailor, Agent: keeper, Session: session, SessionEnded: ended, Content: "Covered knots."})
	if err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if got, found, err := svc.SessionSummary(context.Background(), workspace, sailor, session); !found || err != nil || got.ID != saved.ID {
		t.Errorf("SessionSummary = %+v, %v, %v; want the summary saved", got, found, err)
	}
	if _, found, _ := svc.SessionSummary(context.Background(), workspace, sailor, uuid.NewV7()); found {
		t.Error("another session read back this one's summary")
	}
}

func TestASummaryMissingWhatItIsOfIsRefused(t *testing.T) {
	svc := usercontext.NewService(fake.New())
	whole := usercontext.SummaryRequest{Workspace: workspace, User: sailor, Agent: keeper, Session: uuid.NewV7(), SessionEnded: ended, Content: "Covered knots."}
	for name, change := range map[string]func(*usercontext.SummaryRequest){
		"no workspace":    func(r *usercontext.SummaryRequest) { r.Workspace = uuid.Nil() },
		"no user":         func(r *usercontext.SummaryRequest) { r.User = uuid.Nil() },
		"no agent":        func(r *usercontext.SummaryRequest) { r.Agent = uuid.Nil() },
		"no session":      func(r *usercontext.SummaryRequest) { r.Session = uuid.Nil() },
		"only spaces":     func(r *usercontext.SummaryRequest) { r.Content = " \n " },
		"a NUL character": func(r *usercontext.SummaryRequest) { r.Content = "Knots\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			r := whole
			change(&r)
			if _, err := svc.SaveSummary(context.Background(), r); !errors.Is(err, usercontext.ErrBadRequest) {
				t.Errorf("SaveSummary = %v, want ErrBadRequest", err)
			}
		})
	}
}
