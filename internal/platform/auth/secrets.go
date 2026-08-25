package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
)

// Secret sizes.
const (
	// verificationCodeDigits matches what the email says and what the
	// client's input expects.
	verificationCodeDigits = 6
	// resetTokenBytes is 256 bits of entropy: a reset link is a bearer
	// credential and must not be guessable.
	resetTokenBytes = 32
)

// SecretGenerator produces the codes and tokens that travel by email, using
// crypto/rand. It satisfies the port the auth module declares.
type SecretGenerator struct{}

// NewSecretGenerator builds the generator. It holds no state.
func NewSecretGenerator() SecretGenerator { return SecretGenerator{} }

// VerificationCode returns a uniformly distributed six digit code, zero
// padded. Drawing one number below 10^6 avoids the modulo bias that building
// it digit by digit would introduce.
func (SecretGenerator) VerificationCode() (string, error) {
	limit := big.NewInt(1)
	for range verificationCodeDigits {
		limit.Mul(limit, big.NewInt(10))
	}

	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("draw verification code: %w", err)
	}
	return fmt.Sprintf("%0*d", verificationCodeDigits, value), nil
}

// ResetToken returns a URL safe random token for the reset link.
func (SecretGenerator) ResetToken() (string, error) {
	buffer := make([]byte, resetTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("draw reset token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
