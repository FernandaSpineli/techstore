package auth

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		pw string
		ok bool
	}{
		{"", false},
		{"1234567", false},
		{"12345678", true},
		{strings.Repeat("a", 72), true},
		{strings.Repeat("a", 73), false},
		{strings.Repeat("é", 37), false}, // 74 bytes: the limit is bytes, not characters
	}
	for _, tt := range tests {
		if got := validatePassword(tt.pw) == ""; got != tt.ok {
			t.Errorf("validatePassword(len=%d) ok = %v, want %v", len(tt.pw), got, tt.ok)
		}
	}
}

func TestHasher(t *testing.T) {
	h, err := newHasher(bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := h.hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hash, []byte("correct horse")) {
		t.Fatal("hash contains the plain password")
	}
	if !h.matches(hash, "correct horse") {
		t.Error("matching password rejected")
	}
	if h.matches(hash, "wrong horse") {
		t.Error("wrong password accepted")
	}
	if h.matches(nil, "correct horse") {
		t.Error("nil hash (unknown user) accepted")
	}
}

func TestOpaqueTokens(t *testing.T) {
	a, hashA := newOpaqueToken()
	b, _ := newOpaqueToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if len(a) != 43 { // 32 bytes, base64url without padding
		t.Errorf("token length = %d, want 43", len(a))
	}
	if !bytes.Equal(hashA, hashToken(a)) {
		t.Error("hashToken is not deterministic")
	}
}
