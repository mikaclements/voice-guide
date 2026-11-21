package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mikaclements/voice-guide/internal/access"
	"github.com/mikaclements/voice-guide/internal/api"
)

func TestAKeyWithoutTheRoutesPermissionIsForbiddenAndToldWhichItNeeds(t *testing.T) {
	f := newFixture(t)
	for _, r := range routes {
		if !r.keys {
			continue
		}
		t.Run(r.pattern, func(t *testing.T) {
			method, _, _ := strings.Cut(r.pattern, " ")
			key := f.productKeyWith(t, every&^r.need)
			rec := f.send(method, r.path, "Bearer "+key)
			if !strings.Contains(rec.Body.String(), r.need.String()) {
				t.Errorf("body %s doesn't name the permission %s", rec.Body, r.need)
			}
			assertProblem(t, rec, http.StatusForbidden)
			// As RFC 6750 answers a credential that lacks the scope a resource needs.
			if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer error="insufficient_scope", scope="`+r.need.String()+`"`; got != want {
				t.Errorf("WWW-Authenticate = %q, want %q", got, want)
			}
			if _, ok := f.reached[r.pattern]; ok {
				t.Error("the route's handler ran")
			}
			if strings.Contains(f.logs.String(), key) {
				t.Error("the key is in the log")
			}
		})
	}
}

func TestTheScopeARouteNeedsIsItsPermissionsSpaceSeparated(t *testing.T) {
	f := newFixture(t)
	f.rt.HandleProduct("GET /v1/probe", api.RequestTimeout, every, func(http.ResponseWriter, *http.Request, access.Caller) { t.Error("the handler ran") })
	rec := f.send("GET", "/v1/probe", "Bearer "+f.productKeyWith(t, access.RunSessions))
	// As RFC 6750 lists a scope's names.
	if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer error="insufficient_scope", scope="run_sessions publish_agents"`; rec.Code != http.StatusForbidden || got != want {
		t.Errorf("%d, WWW-Authenticate = %q; want 403 and %q", rec.Code, got, want)
	}
}

func TestAKeyHoldingOnlyTheRoutesPermissionReachesIt(t *testing.T) {
	f := newFixture(t)
	for _, r := range routes {
		if !r.keys {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		want := access.Caller{Workspace: caller.Workspace, Product: true, Permissions: r.need}
		if rec := f.send(method, r.path, "Bearer "+f.productKeyWith(t, r.need)); rec.Code != http.StatusOK || f.reached[r.pattern] != want {
			t.Errorf("%s: %d, reached with %+v; want %+v", r.pattern, rec.Code, f.reached[r.pattern], want)
		}
	}
}

func TestARouteTakingAKeyCantBeRegisteredWithoutAPermission(t *testing.T) {
	f := newFixture(t)
	for name, register := range map[string]func(){
		"HandleProduct":    func() { f.rt.HandleProduct("GET /v1/unguarded", api.RequestTimeout, 0, nil) },
		"HandleSessionEnd": func() { f.rt.HandleSessionEnd("POST /v1/sessions/{id}/unguarded", api.RequestTimeout, 0, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("a route taking a key was registered with no permission")
				}
			}()
			register()
		})
	}
}
