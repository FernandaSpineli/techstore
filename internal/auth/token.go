package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtIssuer   = "techstore"
	jwtAudience = "techstore-api"
)

// accessClaims are the JWT claims of an access token. The subject is the
// user ID.
type accessClaims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// tokenIssuer signs and verifies short-lived HS256 access tokens. Access
// tokens are not checked against the database, so a role change or logout
// takes effect within one TTL; that is the trade-off for stateless requests.
type tokenIssuer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func (t *tokenIssuer) issue(userID string, role Role) (string, error) {
	now := t.now()
	claims := accessClaims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{jwtAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
			ID:        rand.Text(),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return token, nil
}

var errInvalidAccessToken = errors.New("auth: invalid access token")

func (t *tokenIssuer) parse(token string) (Principal, error) {
	var claims accessClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return t.secret, nil },
		// Pinning the algorithm blocks "alg: none" and algorithm-confusion attacks.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithAudience(jwtAudience),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", errInvalidAccessToken, err)
	}
	if claims.Subject == "" || !claims.Role.valid() {
		return Principal{}, fmt.Errorf("%w: missing subject or role", errInvalidAccessToken)
	}
	return Principal{UserID: claims.Subject, Role: claims.Role}, nil
}
