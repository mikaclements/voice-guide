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
