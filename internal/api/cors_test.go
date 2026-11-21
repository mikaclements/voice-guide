package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// request sends method at path with the headers given, an empty one left out.
func (f *fixture) request(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	f.rt.ServeHTTP(rec, req)
	return rec
}

// preflight sends what a browser sends before a page on another site calls
// method at path, with the headers given set over the browser's.
func (f *fixture) preflight(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"Origin": "https://shop.example", "Access-Control-Request-Method": method, "Access-Control-Request-Headers": "authorization,content-type"}
	for k, v := range headers {
		h[k] = v
	}
	return f.request(http.MethodOptions, path, h)
}

// fromSite sends a request as a page on another site does, with authorization if any.
func (f *fixture) fromSite(method, path, authorization string) *httptest.ResponseRecorder {
	return f.request(method, path, map[string]string{"Origin": "https://shop.example", "Authorization": authorization})
}

// assertNoCORS checks rec carries no CORS header, so a browser keeps the answer from the page.
func assertNoCORS(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name := range rec.Header() {
		if strings.HasPrefix(name, "Access-Control-") {
			t.Errorf("%s = %q, want no CORS header", name, rec.Header().Get(name))
		}
	}
}

// Whatever headers a preflight asks for, odd or huge, the answer is the same.
func TestASessionsRoutesAnswerAPreflightFromAnySite(t *testing.T) {
	f := newFixture(t)
	for _, r := range routes {
		if !r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		for _, asked := range []string{"authorization,content-type", "cookie, x-anything", strings.Repeat("x-header-", 8000), ",,, ;\t"} {
			rec := f.preflight(method, r.path, map[string]string{"Access-Control-Request-Headers": asked})
			if h := rec.Header(); rec.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Methods") != method ||
				h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" || h.Get("Access-Control-Max-Age") != "7200" ||
				h.Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("%s asking for %.40q: %d %v; want 204 allowing any origin with no credentials, %s, the ticket's and the body's headers, for 2 hours", r.pattern, asked, rec.Code, h, method)
			}
		}
	}
}

func TestAProductsRoutesRefuseAPreflightAndSendNoCORSHeaders(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	for _, r := range routes {
		if r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		rec := f.preflight(method, r.path, nil)
		assertProblem(t, rec, http.StatusMethodNotAllowed)
		assertNoCORS(t, rec)
		// Sent anyway, as a script can: refused, and no browser lets a page read even that.
		rec = f.fromSite(method, r.path, "Bearer "+key)
		assertProblem(t, rec, http.StatusUnauthorized)
		assertNoCORS(t, rec)
	}
}

func TestAPreflightReachesNoHandlerAndReadsNoKey(t *testing.T) {
	f := newFixture(t)
	credentials := []string{"Bearer " + f.productKey(t), "Bearer " + mint(t, f.key, now)}
	for _, r := range routes {
		method, _, _ := strings.Cut(r.pattern, " ")
		for _, c := range credentials {
			f.preflight(method, r.path, map[string]string{"Authorization": c})
		}
	}
	if len(f.reached) != 0 || f.store.Reads() != 0 {
		t.Errorf("reached %v after %d key reads; want no handler run and no key read", f.reached, f.store.Reads())
	}
}

func TestAnOPTIONSRequestThatIsNoPreflightIsRefused(t *testing.T) {
	f := newFixture(t)
	cases := map[string]map[string]string{
		"no origin":                       {"Origin": ""},
		"no method asked for":             {"Access-Control-Request-Method": ""},
		"a method the route doesn't take": {"Access-Control-Request-Method": "GET"},
		"OPTIONS itself":                  {"Access-Control-Request-Method": "OPTIONS"},
		"a method in another case":        {"Access-Control-Request-Method": "post"},
	}
	for name, headers := range cases {
		rec := f.preflight("POST", "/v1/sessions/"+session+"/turns", headers)
		assertProblem(t, rec, http.StatusMethodNotAllowed)
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: answered as a preflight", name)
		}
	}
	if len(f.reached) != 0 {
		t.Errorf("handlers ran: %v", f.reached)
	}
}

func TestARequestFromAnotherSiteStillNeedsItsTicket(t *testing.T) {
	f := newFixture(t)
	ticket := mint(t, f.key, now)
	for _, r := range routes {
		if !r.tickets {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		rec := f.fromSite(method, r.path, "")
		assertProblem(t, rec, http.StatusUnauthorized)
		// The refusal is readable, so the page says why.
		if _, ran := f.reached[r.pattern]; ran || rec.Header().Get("Access-Control-Allow-Origin") != "*" || rec.Header().Get("Access-Control-Expose-Headers") != "Location, Retry-After" {
			t.Errorf("%s refused: handler ran %v, %v; want it not run, any origin allowed, Location and Retry-After readable", r.pattern, ran, rec.Header())
		}
		if rec := f.fromSite(method, r.path, "Bearer "+ticket); rec.Code != http.StatusOK || f.reached[r.pattern] != holder(ticketCaller) ||
			rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s with its ticket: %d, reached with %+v, %v; want 200 to the ticket's caller, any origin allowed", r.pattern, rec.Code, f.reached[r.pattern], rec.Header())
		}
	}
}

// A product's key from a page has reached every visitor, and a page could
// have its visitors' browsers send guessed keys, so a key sent with an origin
// is refused before it is read, on every route that takes one; the product's
// server, whose requests name no origin, still ends a session with its key.
func TestAProductKeyFromAPageIsRefusedUnread(t *testing.T) {
	f := newFixture(t)
	key := f.productKey(t)
	for _, r := range routes {
		if !r.keys {
			continue
		}
		method, _, _ := strings.Cut(r.pattern, " ")
		rec := f.fromSite(method, r.path, "Bearer "+key)
		assertProblem(t, rec, http.StatusUnauthorized)
		if !strings.Contains(f.logs.String(), "a product key sent from a page") {
			t.Errorf("%s: refused for another reason; logs %q", r.pattern, f.logs.String())
		}
	}
	if len(f.reached) != 0 || f.store.Reads() != 0 {
		t.Errorf("reached %v after %d key reads; want no handler run and no key read", f.reached, f.store.Reads())
	}
	if rec := f.send("POST", "/v1/sessions/"+session+"/end", "Bearer "+key); rec.Code != http.StatusOK ||
		f.reached["POST /v1/sessions/{id}/end"] != keyCaller {
		t.Errorf("the product's server ending a session: %d, reached with %+v; want the product's end", rec.Code, f.reached)
	}
}

// Only OPTIONS is a preflight: a request carrying a preflight's headers is
// still checked and handled as itself.
func TestARequestWithAPreflightsHeadersIsStillARequest(t *testing.T) {
	f := newFixture(t)
	path := "/v1/sessions/" + session + "/turns"
	headers := map[string]string{"Origin": "https://shop.example", "Access-Control-Request-Method": "POST"}
	assertProblem(t, f.request("POST", path, headers), http.StatusUnauthorized)
	headers["Authorization"] = "Bearer " + mint(t, f.key, now)
	if rec := f.request("POST", path, headers); rec.Code != http.StatusOK || f.reached["POST /v1/sessions/{id}/turns"] != holder(ticketCaller) {
		t.Errorf("%d, reached with %+v; want 200 to the ticket's caller", rec.Code, f.reached)
	}
}
