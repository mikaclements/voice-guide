package secret_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mikaclements/voice-guide/internal/secret"
)

// token holds a secret as the packages that keep one declare it.
type token struct {
	heldToken
}

type heldToken = secret.Value[token]

// holder keeps a token in a field fmt and slog can't call its methods on.
type holder struct{ t token }

const (
	raw   = "tok_9c1f2a7e"
	shown = "secret_test.token(redacted)"
)

// Every way a secret could be printed, logged or encoded shows the name of
// the type that holds it, redacted, and only Reveal gives the secret.
func TestASecretShowsOnlyItsHoldersNameRedacted(t *testing.T) {
	tok := token{secret.New[token](raw)}
	_, textMarshaler := any(tok).(encoding.TextMarshaler)
	_, textMarshalerPtr := any(&tok).(encoding.TextMarshaler)
	for _, c := range []struct{ name, got, want string }{
		{"Reveal", tok.Reveal(), raw},
		{"Reveal of the zero value", token{}.Reveal(), ""},
		{"%v", fmt.Sprintf("%v", tok), shown},
		{"%+v", fmt.Sprintf("%+v", tok), shown},
		{"%#v", fmt.Sprintf("%#v", tok), shown},
		{"%s", fmt.Sprintf("%s", tok), shown},
		{"%q", fmt.Sprintf("%q", tok), shown},
		{"%x", fmt.Sprintf("%x", tok), shown},
		{"%d", fmt.Sprintf("%d", tok), shown},
		{"%10v", fmt.Sprintf("%10v", tok), shown},
		{"Sprint", fmt.Sprint(tok), shown},
		{"%v of a pointer", fmt.Sprintf("%v", &tok), shown},
		{"%v of the zero value", fmt.Sprintf("%v", token{}), shown},
		{"%+v of an exported field", fmt.Sprintf("%+v", struct{ T token }{tok}), "{T:" + shown + "}"},
		{"%#v of a slice", fmt.Sprintf("%#v", []token{tok}), "[]secret_test.token{" + shown + "}"},
		{"String", tok.String(), shown},
		{"GoString", tok.GoString(), shown},
		{"LogValue", tok.LogValue().String(), shown},
		{"slog's JSON handler", logged(slog.NewJSONHandler, tok), `{"level":"INFO","msg":"m","secret":"` + shown + `"}` + "\n"},
		{"slog's text handler", logged(slog.NewTextHandler, tok), "level=INFO msg=m secret=" + shown + "\n"},
		{"JSON", encoded(t, tok), `"` + shown + `"`},
		{"JSON of a pointer", encoded(t, &tok), `"` + shown + `"`},
		{"JSON of an exported field", encoded(t, struct{ T token }{tok}), `{"T":"` + shown + `"}`},
		{"JSON of an unexported field", encoded(t, holder{tok}), `{}`},
		{"encoding.TextMarshaler", strconv.FormatBool(textMarshaler || textMarshalerPtr), "false"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// A struct holding a secret where fmt can't call its methods shows where the
// secret is, never the secret.
func TestASecretInAnUnexportedFieldShowsAnAddress(t *testing.T) {
	tok := token{secret.New[token](raw)}
	for name, c := range map[string]struct{ got, want string }{
		"%v":                  {fmt.Sprintf("%v", holder{tok}), `^\{\{\{0x[0-9a-f]+\}\}\}$`},
		"%+v":                 {fmt.Sprintf("%+v", holder{tok}), `^\{t:\{heldToken:\{raw:0x[0-9a-f]+\}\}\}$`},
		"%#v":                 {fmt.Sprintf("%#v", holder{tok}), `\{raw:\(\*string\)\(0x[0-9a-f]+\)\}\}\}$`},
		"slog's text handler": {logged(slog.NewTextHandler, holder{tok}), `^level=INFO msg=m secret=\{t:\{heldToken:\{raw:0x[0-9a-f]+\}\}\}\n$`},
	} {
		if !regexp.MustCompile(c.want).MatchString(c.got) || strings.Contains(c.got, raw) {
			t.Errorf("%s = %s, want only an address", name, c.got)
		}
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
