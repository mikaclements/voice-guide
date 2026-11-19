package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const script = "customElements.define('x-lamp', class extends HTMLElement {});\n"

// clientDir returns a folder holding the drop-in client as body, none if
// empty, with a file beside the folder that must never be served.
func clientDir(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist-client")
	write(t, filepath.Join(dir, "..", "secret.txt"), "the key is under the mat")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		write(t, filepath.Join(dir, "voice-guide.js"), body)
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const clientPath = "/client/voice-guide.js"

func TestTheClientIsServedAsAScriptAnySiteMayLoad(t *testing.T) {
	f := newFixture(t)
	f.rt.ServeClient(clientDir(t, script))
	rec := f.request("GET", clientPath, map[string]string{"Origin": "https://shop.example"})
	h := rec.Header()
	if rec.Code != http.StatusOK || rec.Body.String() != script {
		t.Fatalf("%d %q, want 200 with the script", rec.Code, rec.Body.String())
	}
	if h.Get("Content-Type") != "text/javascript; charset=utf-8" || h.Get("Cache-Control") != "no-cache" ||
		h.Get("Access-Control-Allow-Origin") != "*" || !strings.HasPrefix(h.Get("ETag"), `"`) || h.Get("Last-Modified") != "" {
		t.Errorf("headers %v; want JavaScript in UTF-8, checked at each load by its content's tag, open to any site", h)
	}
	if again := f.request("GET", clientPath, map[string]string{"If-None-Match": h.Get("ETag")}); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("asked again with its tag: %d with %d bytes, want 304 with none", again.Code, again.Body.Len())
	}
	if head := f.request("HEAD", clientPath, nil); head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != h.Get("ETag") {
		t.Errorf("HEAD: %d with %d bytes, want 200 with its headers and no body", head.Code, head.Body.Len())
	}
	for _, method := range []string{"POST", "OPTIONS"} {
		rec := f.request(method, clientPath, nil)
		assertProblem(t, rec, http.StatusMethodNotAllowed)
		if rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: Allow = %q, want GET, HEAD", method, rec.Header().Get("Allow"))
		}
	}
	if len(f.reached) != 0 || f.store.Reads() != 0 {
		t.Errorf("reached %v after %d key reads; the script takes no credential", f.reached, f.store.Reads())
	}
}

func TestTheClientIsNotFoundUntilItIsBuilt(t *testing.T) {
	for name, dir := range map[string]string{
		"a folder without it":         clientDir(t, ""),
		"a folder that doesn't exist": filepath.Join(t.TempDir(), "never-built"),
		"no folder set":               "",
	} {
		f := newFixture(t)
		f.rt.ServeClient(dir)
		assertProblem(t, f.request("GET", clientPath, nil), http.StatusNotFound)
		if f.logs.Len() != 0 {
			t.Errorf("%s: logged %q; a client not yet built is expected", name, f.logs.String())
		}
	}
}

func TestNoOtherFileIsServed(t *testing.T) {
	f := newFixture(t)
	dir := clientDir(t, script)
	write(t, filepath.Join(dir, "other.js"), "other")
	f.rt.ServeClient(dir)
	for _, path := range []string{
		"/client/other.js", "/client/", "/client/voice-guide.js/", "/client/../secret.txt", "/client/%2e%2e/secret.txt",
		"/client/voice-guide.js/../../secret.txt", "/client//voice-guide.js", "/client/voice-guide.js%00",
	} {
		rec := f.request("GET", path, nil)
		assertProblem(t, rec, http.StatusNotFound)
		if body := rec.Body.String(); strings.Contains(body, "mat") || strings.Contains(body, "other") {
			t.Errorf("%s served %q", path, body)
		}
	}
}

// Whatever stands in the client's place, only a file in its folder is served.
func TestOnlyAFileInTheFolderIsServed(t *testing.T) {
	cases := []struct {
		name   string
		place  func(dir, file string) error
		served bool
	}{
		{"a link out of the folder", func(dir, file string) error { return os.Symlink(filepath.Join(dir, "..", "secret.txt"), file) }, false},
		{"a folder", func(_, file string) error { return os.Mkdir(file, 0o755) }, false},
		{"a link to a file in the folder", func(dir, file string) error {
			write(t, filepath.Join(dir, "voice-guide.1a2b.js"), script)
			return os.Symlink("voice-guide.1a2b.js", file)
		}, true},
	}
	for _, c := range cases {
		f := newFixture(t)
		dir := clientDir(t, "")
		if err := c.place(dir, filepath.Join(dir, "voice-guide.js")); err != nil {
			t.Fatal(err)
		}
		f.rt.ServeClient(dir)
		rec := f.request("GET", clientPath, nil)
		if served := rec.Code == http.StatusOK && rec.Body.String() == script; served != c.served || strings.Contains(rec.Body.String(), "mat") {
			t.Errorf("%s: %d %q, want served %v", c.name, rec.Code, rec.Body.String(), c.served)
		}
		if !c.served && !strings.Contains(f.logs.String(), "read the drop-in client") {
			t.Errorf("%s: refused without logging the folder's mistake", c.name)
		}
	}
}

func TestANewBuildIsServedAtOnceWithItsOwnTag(t *testing.T) {
	f := newFixture(t)
	dir := clientDir(t, script)
	f.rt.ServeClient(dir)
	first := f.request("GET", clientPath, nil).Header().Get("ETag")
	// A build puts the new file in the old one's place.
	write(t, filepath.Join(dir, "next.js"), script+"// rebuilt\n")
	if err := os.Rename(filepath.Join(dir, "next.js"), filepath.Join(dir, "voice-guide.js")); err != nil {
		t.Fatal(err)
	}
	rec := f.request("GET", clientPath, map[string]string{"If-None-Match": first})
	if rec.Code != http.StatusOK || rec.Body.String() != script+"// rebuilt\n" || rec.Header().Get("ETag") == first {
		t.Errorf("%d %q tagged %s after %s; want the new build in full, with a new tag", rec.Code, rec.Body.String(), rec.Header().Get("ETag"), first)
	}
}

// Instances serving one build give one tag, though each wrote its file at its
// own time, so a page checking with one isn't sent the script again by another.
func TestOneBuildHasOneTagOnEveryInstance(t *testing.T) {
	a, b := newFixture(t), newFixture(t)
	dirA, dirB := clientDir(t, script), clientDir(t, script)
	if err := os.Chtimes(filepath.Join(dirB, "voice-guide.js"), time.Time{}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	a.rt.ServeClient(dirA)
	b.rt.ServeClient(dirB)
	tag := a.request("GET", clientPath, nil).Header().Get("ETag")
	if rec := b.request("GET", clientPath, map[string]string{"If-None-Match": tag}); rec.Code != http.StatusNotModified {
		t.Errorf("the other instance answered %d to the same build's tag, want 304", rec.Code)
	}
}

// A pipe in the client's place is refused without being opened: opening one
// waits for a writer, which would hold the request for ever.
func TestAPipeInTheClientsPlaceIsRefusedAtOnce(t *testing.T) {
	f := newFixture(t)
	dir := clientDir(t, "")
	pipe := filepath.Join(dir, "voice-guide.js")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Fatal(err)
	}
	f.rt.ServeClient(dir)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.request("GET", clientPath, nil) }()
	select {
	case rec := <-done:
		assertProblem(t, rec, http.StatusNotFound)
	case <-time.After(5 * time.Second):
		// A writer lets the request go, so the test ends.
		if w, err := os.OpenFile(pipe, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		<-done
		t.Fatal("the request waited on the pipe")
	}
}

// A file the same in identity, size and time is served from what was read
// before, not read again; one whose time moved on is read anew.
func TestTheClientIsReadAgainOnlyOnceItChanges(t *testing.T) {
	f := newFixture(t)
	dir := clientDir(t, script)
	file := filepath.Join(dir, "voice-guide.js")
	f.rt.ServeClient(dir)
	f.request("GET", clientPath, nil)
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	// Rewritten in place, as long, its time put back: no build leaves that, so what was read stands.
	other := strings.Repeat("x", len(script))
	write(t, file, other)
	if err := os.Chtimes(file, time.Time{}, before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if rec := f.request("GET", clientPath, nil); rec.Body.String() != script {
		t.Errorf("served %q, want what was read before", rec.Body.String())
	}
	if err := os.Chtimes(file, time.Time{}, before.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if rec := f.request("GET", clientPath, nil); rec.Body.String() != other {
		t.Errorf("served %q once its time moved on, want it read anew", rec.Body.String())
	}
}
