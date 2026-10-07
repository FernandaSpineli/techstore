package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestSMTPSenderDeliversToARealServer sends through Mailpit, the SMTP
// server docker compose uses, and reads the message back from its API.
func TestSMTPSenderDeliversToARealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker (skipped with -short)")
	}
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, "axllent/mailpit:v1.31.4",
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/livez").WithPort("8025/tcp")),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	smtpAddr, err := ctr.PortEndpoint(ctx, "1025/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	apiURL, err := ctr.PortEndpoint(ctx, "8025/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}

	sender := &SMTPSender{Addr: smtpAddr, From: "TechStore <no-reply@techstore.local>"}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = sender.Send(sendCtx, Message{
		To:      "ana@example.com",
		Subject: "Reset your TechStore password",
		Body:    "Olá Ana,\nUse o link abaixo.",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	resp, err := http.Get(apiURL + "/api/v1/messages") //nolint:noctx // test helper
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Messages []struct {
			ID      string
			Subject string
			From    struct{ Address string }
			To      []struct{ Address string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 {
		t.Fatalf("%d messages delivered, want 1", len(list.Messages))
	}
	m := list.Messages[0]
	got := fmt.Sprintf("%s|%s|%s", m.Subject, m.From.Address, m.To[0].Address)
	if want := "Reset your TechStore password|no-reply@techstore.local|ana@example.com"; got != want {
		t.Errorf("message = %s, want %s", got, want)
	}

	text, err := http.Get(apiURL + "/api/v1/message/" + m.ID) //nolint:noctx // test helper
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = text.Body.Close() }()
	var full struct{ Text string }
	if err := json.NewDecoder(text.Body).Decode(&full); err != nil {
		t.Fatal(err)
	}
	// SMTP uses CRLF line endings and ends the data with one.
	if body := strings.TrimSpace(strings.ReplaceAll(full.Text, "\r\n", "\n")); body != "Olá Ana,\nUse o link abaixo." {
		t.Errorf("body = %q", full.Text)
	}
}
