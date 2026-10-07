package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewWritesEventKey(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, slog.LevelInfo).Info("order.created", "order_id", "123")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if line["event"] != "order.created" {
		t.Errorf("event = %v, want order.created", line["event"])
	}
	if _, ok := line["msg"]; ok {
		t.Error("log line still has a msg key")
	}
	if line["order_id"] != "123" {
		t.Errorf("order_id = %v, want 123", line["order_id"])
	}
}

func TestFromContext(t *testing.T) {
	if FromContext(context.Background()) != slog.Default() {
		t.Error("FromContext without logger should return slog.Default()")
	}
	l := New(&bytes.Buffer{}, slog.LevelInfo)
	if FromContext(WithLogger(context.Background(), l)) != l {
		t.Error("FromContext did not return the stored logger")
	}
}
