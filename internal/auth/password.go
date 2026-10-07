package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultBcryptCost is the production hashing cost (~250ms per hash on a
// modern CPU): slow enough to make offline cracking expensive.
const DefaultBcryptCost = 12

const (
	minPasswordLen = 8
	// bcrypt only uses the first 72 bytes; rejecting longer passwords avoids
	// silently ignoring the rest.
	maxPasswordLen = 72
)

func validatePassword(pw string) string {
	switch {
	case len(pw) < minPasswordLen:
		return fmt.Sprintf("must be at least %d characters", minPasswordLen)
	case len(pw) > maxPasswordLen:
		return fmt.Sprintf("must be at most %d bytes", maxPasswordLen)
	}
	return ""
}

type hasher struct {
	cost int
	// dummy is compared against when the email is unknown, so a failed login
	// takes the same time whether or not the account exists.
	dummy []byte
}

func newHasher(cost int) (*hasher, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), cost)
	if err != nil {
		return nil, fmt.Errorf("auth: init hasher: %w", err)
	}
	return &hasher{cost: cost, dummy: dummy}, nil
}

func (h *hasher) hash(pw string) ([]byte, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), h.cost)
	if err != nil {
		return nil, fmt.Errorf("auth: hash password: %w", err)
	}
	return b, nil
}

// matches reports whether pw matches hash. A nil hash burns the same CPU
// time against the dummy hash and returns false.
func (h *hasher) matches(hash []byte, pw string) bool {
	if hash == nil {
		_ = bcrypt.CompareHashAndPassword(h.dummy, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(pw)) == nil
}

// newOpaqueToken returns a random URL-safe token and its SHA-256 hash. Only
// the hash is stored: tokens have 256 bits of entropy, so a fast hash is
// enough and a leaked table yields nothing usable.
func newOpaqueToken() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
