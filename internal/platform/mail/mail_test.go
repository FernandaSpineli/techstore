package mail

import (
	"context"
	"strings"
	"testing"
)

func TestSendRejectsHeaderInjection(t *testing.T) {
	s := &SMTPSender{Addr: "127.0.0.1:1", From: "TechStore <no-reply@techstore.local>"}
	err := s.Send(context.Background(), Message{To: "a@example.com\r\nBcc: victim@example.com", Subject: "x"})
	if err == nil || !strings.Contains(err.Error(), "header injection") {
		t.Fatalf("err = %v, want header injection error", err)
	}
}

func TestCompose(t *testing.T) {
	got := string(compose("TechStore <no-reply@techstore.local>", Message{
		To: "ana@example.com", Subject: "Hello", Body: "line 1\nline 2",
	}))
	for _, want := range []string{
		"From: TechStore <no-reply@techstore.local>\r\n",
		"To: ana@example.com\r\n",
		"Subject: Hello\r\n",
		"\r\n\r\nline 1\r\nline 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	if addressOf("TechStore <no-reply@techstore.local>") != "no-reply@techstore.local" {
		t.Error("addressOf did not extract the bare address")
	}
}
