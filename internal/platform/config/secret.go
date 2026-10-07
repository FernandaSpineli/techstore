package config

import "log/slog"

const redacted = "[REDACTED]"

// Secret holds a sensitive value such as a connection string or API key.
// It redacts itself when printed, formatted or logged; call Reveal at the
// exact point where the raw value is needed.
type Secret string

// Reveal returns the raw secret value.
func (s Secret) Reveal() string { return string(s) }

func (s Secret) String() string { return redacted }

func (s Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText keeps the secret out of JSON and other text encodings.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
