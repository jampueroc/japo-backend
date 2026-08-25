// Package auth provides the concrete security primitives of the platform:
// stateless JWT access tokens and bcrypt password hashing. Modules depend on
// the narrow interfaces declared in their own domain.go, never on this
// package, except at the composition root.
package auth

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Token related sentinel errors.
var (
	// ErrInvalidToken covers a malformed, wrongly signed or otherwise
	// unusable token.
	ErrInvalidToken = errors.New("invalid token")
	// ErrTokenExpired is returned when the token is well formed but past
	// its expiry.
	ErrTokenExpired = errors.New("token expired")
)

// Claims is the payload of an access token. There are no refresh tokens: the
// token is short lived and the client simply logs in again.
type Claims struct {
	UserID int64 `json:"uid"`
	jwt.RegisteredClaims
}

// JWTManager issues and verifies HS256 access tokens.
type JWTManager struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

// NewJWTManager builds the manager. The secret must be non empty; length is
// validated by the config package at startup.
func NewJWTManager(secret, issuer string, ttl time.Duration) *JWTManager {
	return &JWTManager{secret: []byte(secret), issuer: issuer, ttl: ttl}
}

// TTL is the lifetime of the tokens issued by this manager.
func (m *JWTManager) TTL() time.Duration { return m.ttl }

// Issue signs a token for userID and returns it with its expiry. The
// signature matches the TokenIssuer interface declared by the auth module.
func (m *JWTManager) Issue(userID int64) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(m.ttl)

	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify parses and validates a token, returning its claims.
func (m *JWTManager) Verify(token string) (*Claims, error) {
	claims := &Claims{}

	_, err := jwt.ParseWithClaims(token, claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidToken, t.Header["alg"])
			}
			return m.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if claims.UserID <= 0 {
		// Fall back to the standard subject claim before giving up.
		id, convErr := strconv.ParseInt(claims.Subject, 10, 64)
		if convErr != nil || id <= 0 {
			return nil, fmt.Errorf("%w: missing user identifier", ErrInvalidToken)
		}
		claims.UserID = id
	}

	return claims, nil
}
