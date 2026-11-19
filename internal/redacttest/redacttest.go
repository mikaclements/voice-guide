// Package redacttest checks, for a test, that a value holding a secret never
// shows it: printed, logged or encoded, it shows only its redacted text.
package redacttest

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// Check fails t if v shows anything but shown where code could print, log or
// encode it, or if any of those shows secret.
func Check(t *testing.T, v any, secret, shown string) {
	t.Helper()
	asJSON, asText := logged(v)
	cases := []struct{ name, got, want string }{
		{"%v", fmt.Sprintf("%v", v), shown},
		{"%+v", fmt.Sprintf("%+v", v), shown},
		{"%#v", fmt.Sprintf("%#v", v), shown},
		{"%s", fmt.Sprintf("%s", v), shown},
		{"%q", fmt.Sprintf("%q", v), shown},
		{"%x", fmt.Sprintf("%x", v), shown},
		{"%d", fmt.Sprintf("%d", v), shown},
		{"%10v", fmt.Sprintf("%10v", v), shown},
		{"Sprint", fmt.Sprint(v), shown},
		{"%+v of an exported field", fmt.Sprintf("%+v", struct{ T any }{v}), "{T:" + shown + "}"},
		{"%#v in a slice", fmt.Sprintf("%#v", []any{v}), "[]interface {}{" + shown + "}"},
		{"slog's JSON handler", asJSON, `{"level":"INFO","msg":"m","secret":"` + shown + `"}` + "\n"},
		{"slog's text handler", asText, "level=INFO msg=m secret=" + shown + "\n"},
		{"JSON", encoded(t, v), `"` + shown + `"`},
		{"JSON of an exported field", encoded(t, struct{ T any }{v}), `{"T":"` + shown + `"}`},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
		if strings.Contains(c.got, secret) {
			t.Errorf("%s shows the secret", c.name)
		}
	}
	// fmt calls Format before String or GoString, so the cases above never
	// reach them; code that calls them directly would.
	if s, ok := v.(fmt.Stringer); !ok || s.String() != shown {
		t.Errorf("String: want %q from a fmt.Stringer", shown)
	}
	if g, ok := v.(fmt.GoStringer); !ok || g.GoString() != shown {
		t.Errorf("GoString: want %q from a fmt.GoStringer", shown)
	}
	if l, ok := v.(slog.LogValuer); !ok || l.LogValue().String() != shown {
		t.Errorf("LogValue: want %q from a slog.LogValuer", shown)
	}
	// encoding/json uses a text marshaler for a map key, which would skip the
	// redacted JSON.
	if _, ok := v.(encoding.TextMarshaler); ok {
		t.Errorf("%T is an encoding.TextMarshaler", v)
	}
}

// logged returns the lines slog's JSON and text handlers write for a message
// with v, without the time.
func logged(v any) (asJSON, asText string) {
	var j, x bytes.Buffer
	opts := &slog.HandlerOptions{ReplaceAttr: noTime}
	slog.New(slog.NewJSONHandler(&j, opts)).Info("m", "secret", v)
	slog.New(slog.NewTextHandler(&x, opts)).Info("m", "secret", v)
	return j.String(), x.String()
}

func noTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}

func encoded(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}
