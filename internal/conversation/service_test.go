package conversation_test

import (
	"context"
	"slices"
	"testing"
	"uuid"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/agents"
	agentsfake "github.com/mikaclements/voice-guide/internal/agents/store/fake"
	"github.com/mikaclements/voice-guide/internal/conversation"
	sessionsfake "github.com/mikaclements/voice-guide/internal/conversation/store/fake"
	"github.com/mikaclements/voice-guide/internal/usercontext"
	itemsfake "github.com/mikaclements/voice-guide/internal/usercontext/store/fake"
)

var (
	workspace = uuid.MustParse("01a1de8a-0000-7000-8000-000000000001")
	user      = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000a1")
	keeperID  = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b1")
	captainID = uuid.MustParse("01a1de8a-0000-7000-8000-0000000000b2")
)

// serviceWithTwoAgents returns the service over a fake store holding the
// lighthouse keeper and the ferry captain, both published in the workspace.
func serviceWithTwoAgents(t *testing.T) *conversation.Service {
	t.Helper()
	store := agentsfake.New()
	if _, err := agents.Publish(context.Background(), store, workspace, []agents.Agent{
		{ID: keeperID, Name: "Lighthouse keeper", Persona: "You answer every call from the sea."},
		{ID: captainID, Name: "Ferry captain", Persona: "You know every crossing by heart."},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return conversation.NewService(agents.NewCatalog(store), sessionsfake.New(), nil, nil, usercontext.NewService(itemsfake.New()), conversation.Settings{UserContextChars: contextLimit, SessionContextChars: contextLimit})
}

// agentsFor returns the IDs of the agents the caller may use, in the order listed.
func agentsFor(t *testing.T, s *conversation.Service, c access.Caller) []uuid.UUID {
	t.Helper()
	list, err := s.Agents(context.Background(), c)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	ids := make([]uuid.UUID, len(list))
	for i, v := range list {
		ids[i] = v.ID
	}
	return ids
}

func TestACallerSeesTheWorkspacesAgents(t *testing.T) {
	got := agentsFor(t, serviceWithTwoAgents(t), access.Caller{Workspace: workspace, Product: true})
	if want := []uuid.UUID{captainID, keeperID}; !slices.Equal(got, want) {
		t.Errorf("Agents = %v, want the ferry captain, then the lighthouse keeper", got)
	}
}

func TestACallerFromAnotherWorkspaceSeesNoAgents(t *testing.T) {
	other := uuid.MustParse("01a1de8a-0000-7000-8000-000000000002")
	if got := agentsFor(t, serviceWithTwoAgents(t), access.Caller{Workspace: other, Product: true}); len(got) != 0 {
		t.Errorf("Agents = %v, want none", got)
	}
}
