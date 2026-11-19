package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"time"
)

// ClientPath is where voice serves the drop-in client: the one script a
// product's page loads, from whatever site it is on, to hold the conversation
// in a session the product's server started.
const ClientPath = "/client/voice-guide.js"

// clientFile is the drop-in client's file, the only one served from its folder.
const clientFile = "voice-guide.js"

// client is the drop-in client's folder, and the last build read from it.
type client struct {
	dir  string
	mu   sync.Mutex
	read os.FileInfo // the file the bytes were read from, as it was then
	body []byte
	tag  string
}

// ServeClient has the router serve the drop-in client at ClientPath from the
// folder dir, looked at for each request, so a new build is served without a
// restart; until one is built there, the path is answered 404. It takes no
// credential, since the script holds none, and any site's page may load it.
// Only the one file is served, by its own name, never by a path from the
// request, and a link leading out of the folder isn't followed.
func (rt *Router) ServeClient(dir string) { rt.client = &client{dir: dir} }

func (rt *Router) serveClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		rt.Problem(w, r, http.StatusMethodNotAllowed, "")
		return
	}
	script, tag, err := rt.client.current()
	if err != nil {
		// Not built yet is expected, in a fresh clone; anything else is the folder set up wrong.
		if !errors.Is(err, fs.ErrNotExist) {
			rt.log.WarnContext(r.Context(), "read the drop-in client", "err", err)
		}
		rt.Problem(w, r, http.StatusNotFound, "the drop-in client isn't built")
		return
	}
	h := w.Header()
	// The charset too: a script with none is read in the encoding of the page that loads it.
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	// Every load asks whether the script changed, answered 304 with no body when it hasn't: a new
	// build reaches every page at its next load, and in development at the next reload. The tag is
	// the content's own, never a time, so every instance serving one build gives the same one, and
	// instances serving different builds during a deploy give each page one whole build.
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", tag)
	// A module script loads in CORS mode, so a page on another site needs this to run it.
	h.Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, clientFile, time.Time{}, bytes.NewReader(script))
}

// current returns the client's build and its tag: the bytes read before while
// the file is the same one, with the same size and time, or else read anew,
// whole, from one open file, so an answer is one build. A link out of the
// folder is refused, and so is anything but a plain file, checked before it is
// opened, since opening a pipe would wait for a writer.
func (c *client) current() ([]byte, string, error) {
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(clientFile)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s in %s is not a file", clientFile, c.dir)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.read != nil && os.SameFile(c.read, info) && c.read.Size() == info.Size() && c.read.ModTime().Equal(info.ModTime()) {
		return c.body, c.tag, nil
	}
	f, err := root.Open(clientFile)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	// The file opened is the one kept as read, which a build may have put in place since the look above.
	if info, err = f.Stat(); err != nil {
		return nil, "", err
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	c.read, c.body, c.tag = info, body, `"`+hex.EncodeToString(sum[:16])+`"`
	return c.body, c.tag, nil
}
