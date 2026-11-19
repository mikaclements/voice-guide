package agents

import (
	"testing"
	"uuid"
)

// An agent saved before session context existed must keep its hash, or the next
// publish would add a version of every agent although no file changed.
func TestAnAgentWithoutSessionContextKeepsItsEarlierHash(t *testing.T) {
	keeper := Agent{
		ID: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1"), Name: "Lighthouse keeper",
		Description: "Talks ships through the fog.", Reads: []string{"profile"}, Persona: "You answer every call from the sea.",
	}
	const earlier = "b3bfbfce10336c90e470b851f8921e5ac0a428577106a42f456b2518afe24806"
	if got := contentHash(keeper); got != earlier {
		t.Errorf("hash = %s, want the hash it had before session context existed", got)
	}
}

func gradedForHash() Agent {
	return Agent{
		ID: uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1"), Name: "Lighthouse keeper",
		Persona: "You answer every call from the sea.", Summary: "Note the ships.",
		Grading: Grading{Lowest: 1, Highest: 5, Criteria: []Criterion{{Name: "fog_calls", LooksFor: "Answers every call."}}},
	}
}

// The hash names a grading's parts by keys of its own, so renaming a Go field can't give every
// graded agent a new version.
func TestAGradedAgentsHashIsPinned(t *testing.T) {
	const pinned = "8c60d56a5323928bacacad91518a4c7b9f479d2380863839747f6f3bea321d0f"
	if got := contentHash(gradedForHash()); got != pinned {
		t.Errorf("hash = %s, want %s", got, pinned)
	}
}

func TestNoLevelsHashAlikeHoweverTheyWereWritten(t *testing.T) {
	none, empty := gradedForHash(), gradedForHash()
	empty.Grading.Levels = []Level{}
	if contentHash(none) != contentHash(empty) {
		t.Error("nil and empty levels hash differently, want the same hash")
	}
}
