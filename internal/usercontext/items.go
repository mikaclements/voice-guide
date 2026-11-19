package usercontext

import (
	"context"
	"errors"
	"fmt"
	"uuid"
)

// ItemsRequest asks for some of a user's items by ID, such as those a session
// loaded when it started.
type ItemsRequest struct {
	Workspace uuid.UUID
	User      uuid.UUID
	IDs       []uuid.UUID
}

// ErrNoSuchItem reports an ID that isn't one of the user's items in the
// workspace. Another user's item looks the same as one that doesn't exist.
var ErrNoSuchItem = errors.New("no such context item")

// Items returns the items with the requested IDs, in the order asked for. An
// item replaced since is still returned: a session keeps what it loaded, so
// its prompt stays the same from turn to turn.
func (s *Service) Items(ctx context.Context, r ItemsRequest) ([]Item, error) {
	found, err := s.items.ByID(ctx, r.Workspace, r.User, r.IDs)
	if err != nil {
		return nil, fmt.Errorf("read the user's items: %w", err)
	}
	byID := make(map[uuid.UUID]Item, len(found))
	for _, it := range found {
		byID[it.ID] = it
	}
	out := make([]Item, 0, len(r.IDs))
	for _, id := range r.IDs {
		it, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoSuchItem, id)
		}
		out = append(out, it)
	}
	return out, nil
}
