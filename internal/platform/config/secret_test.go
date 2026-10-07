package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverLeaks(t *testing.T) {
	s := Secret("postgres://user:hunter2@db/app")
	cfg := struct{ DSN Secret }{DSN: s}

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("cfg", "dsn", s, "cfg", cfg)
	encoded, _ := json.Marshal(cfg)

	outputs := map[string]string{
		"%v":   fmt.Sprintf("%v", s),
		"%s":   fmt.Sprintf("%s", s),
		"%#v":  fmt.Sprintf("%#v", cfg),
		"%+v":  fmt.Sprintf("%+v", cfg),
		"slog": logs.String(),
		"json": string(encoded),
	}
	for name, out := range outputs {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
	}
	if s.Reveal() != "postgres://user:hunter2@db/app" {
		t.Error("Reveal() did not return the raw value")
	}
}
