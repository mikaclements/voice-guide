// Package secret holds a secret, such as a key or a ticket, so it formats,
// logs and encodes redacted and never reaches a log or an error; Reveal gives
// it, to show it once, send it or sign with it. A type that holds a secret
// embeds a Value, so how a secret is kept out of sight is written once. It
// imports only the standard library, so anything may hold a secret.
package secret

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
)

// Value is a secret held by T, the type that embeds it, and shows itself as
// T's name, then "(redacted)", so a log line still says what was there. The
// secret sits behind a pointer, so even printing a struct that holds one
// where its methods can't be called shows an address, not the secret. It has
// no MarshalText, so nothing that encodes text reaches the secret either.
type Value[T any] struct {
	raw *string
}

// New holds raw as T's secret.
func New[T any](raw string) Value[T] { return Value[T]{raw: &raw} }

// Reveal returns the secret itself.
func (v Value[T]) Reveal() string {
	if v.raw == nil {
		return ""
	}
	return *v.raw
}

// redacted is how a Value shows itself everywhere but Reveal.
func redacted[T any]() string { return reflect.TypeFor[T]().String() + "(redacted)" }

// String keeps the secret out of formatted output.
func (Value[T]) String() string { return redacted[T]() }

// GoString keeps the secret out of %#v.
func (Value[T]) GoString() string { return redacted[T]() }

// Format keeps the secret out of every formatting verb.
func (Value[T]) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, redacted[T]()) }

// LogValue keeps the secret out of logs.
func (Value[T]) LogValue() slog.Value { return slog.StringValue(redacted[T]()) }

// MarshalJSON keeps the secret out of anything encoded as JSON.
func (Value[T]) MarshalJSON() ([]byte, error) { return json.Marshal(redacted[T]()) }
