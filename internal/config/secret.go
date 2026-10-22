package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive value. It redacts itself when formatted, logged,
// or marshaled so that it cannot leak through logs or API responses by
// accident. Call Reveal only at the point where the raw value is required.
type Secret string

// Reveal returns the raw secret value.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret has a non-empty value.
func (s Secret) IsSet() bool { return s != "" }

// String implements fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer so %#v is redacted too.
func (s Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText implements encoding.TextMarshaler (used by JSON and others).
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
