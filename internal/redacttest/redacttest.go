// Package redacttest checks, for a test, that a value holding a secret never
// shows it: printed, logged or encoded, it shows only its redacted text.
package redacttest

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// Check fails t if v shows anything but shown where code could print, log or
// encode it, or if any of those shows secret.
func Check(t *testing.T, v any, secret, shown string) {
	t.Helper()
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
		{"slog's JSON handler", logged(slog.NewJSONHandler, v), `{"level":"INFO","msg":"m","secret":"` + shown + `"}` + "\n"},
		{"slog's text handler", logged(slog.NewTextHandler, v), "level=INFO msg=m secret=" + shown + "\n"},
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
	// encoding/json uses a text marshaler for a map key, which would skip the
	// redacted JSON.
	if _, ok := v.(encoding.TextMarshaler); ok {
		t.Errorf("%T is an encoding.TextMarshaler", v)
	}
}

// logged returns the line a handler writes for a message with v, without its time.
func logged[H slog.Handler](handler func(io.Writer, *slog.HandlerOptions) H, v any) string {
	var b bytes.Buffer
	noTime := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			return slog.Attr{}
		}
		return a
	}
	slog.New(handler(&b, &slog.HandlerOptions{ReplaceAttr: noTime})).Info("m", "secret", v)
	return b.String()
}

func encoded(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}
