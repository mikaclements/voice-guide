package access

import (
	"errors"
	"fmt"
	"strings"
)

// Permissions is what a product key lets its holder do, fixed when the key is
// made: each route that takes a key needs one. A set of bits, so a Caller
// holding one is still a plain value that compares with ==.
type Permissions uint8

const (
	// RunSessions lets a product's server run its sessions: list the agents
	// and voices, start and end sessions and read past ones.
	RunSessions Permissions = 1 << iota
	// PublishAgents lets its holder use the agent routes, those on one agent.
	// A key that runs sessions all day on a product's server need not hold it.
	PublishAgents
)

// ParsePermissions reads permissions by name, as the database keeps them and
// the product key command takes them, refusing an unknown name or none.
func ParsePermissions(names []string) (Permissions, error) {
	var p Permissions
	for _, name := range names {
		switch name {
		case "run_sessions":
			p |= RunSessions
		case "publish_agents":
			p |= PublishAgents
		default:
			return 0, fmt.Errorf("unknown permission %q: the permissions are run_sessions and publish_agents", name)
		}
	}
	if p == 0 {
		return 0, errors.New("a product key needs at least one permission")
	}
	return p, nil
}

// Names returns the permissions' names, as ParsePermissions reads them.
func (p Permissions) Names() []string {
	var names []string
	if p.Has(RunSessions) {
		names = append(names, "run_sessions")
	}
	if p.Has(PublishAgents) {
		names = append(names, "publish_agents")
	}
	return names
}

// String names the permissions, joined by commas.
func (p Permissions) String() string { return strings.Join(p.Names(), ",") }

// Has reports whether p holds every permission in need.
func (p Permissions) Has(need Permissions) bool { return p&need == need }
