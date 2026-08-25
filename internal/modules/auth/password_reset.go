package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ForgotPassword emails a reset link. It always reports success: whether an
// address is registered is not something this endpoint will tell you.
func (s *service) ForgotPassword(ctx context.Context, email string) error {
	address, ok := silentEmail(email)
	if !ok {
		return nil
	}

	user, err := s.repo.FindByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			s.logger.DebugContext(ctx, "password reset requested for an unknown address")
			return nil
		}
		return fmt.Errorf("forgot password: look up account: %w", err)
	}

	token, err := s.secrets.ResetToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}

	now := s.clock().UTC()
	reset := PasswordReset{
		UserID:    user.ID,
		TokenHash: hashSecret(token),
		ExpiresAt: now.Add(s.policy.ResetTokenTTL),
	}
	if err := s.resets.PutReset(ctx, reset); err != nil {
		return fmt.Errorf("store reset token: %w", err)
	}

	// Logged, never returned: the response must look the same whether or not
	// the address exists, and a mail outage is our problem, not a signal to
	// hand back to an anonymous caller.
	if sendErr := s.notifier.SendPasswordReset(ctx, user.Email.String(), token); sendErr != nil {
		s.logger.ErrorContext(ctx, "could not send the password reset email",
			slog.Int64("user_id", user.ID.Int64()), slog.Any("error", sendErr))
	} else {
		s.logger.InfoContext(ctx, "password reset link sent",
			slog.Int64("user_id", user.ID.Int64()),
			slog.Duration("ttl", s.policy.ResetTokenTTL),
		)
	}

	return nil
}

// ResetPassword redeems a token and replaces the password. The token is
// single use, so a leaked link in a browser history or a mail archive stops
// working the moment it is spent.
func (s *service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := checkPasswordLength(newPassword); err != nil {
		return err
	}

	reset, err := s.resets.FindResetByHash(ctx, hashSecret(token))
	if err != nil {
		if errors.Is(err, ErrResetNotFound) {
			return ErrInvalidResetToken
		}
		return fmt.Errorf("reset password: look up token: %w", err)
	}

	now := s.clock().UTC()
	if !reset.Usable(now) {
		return ErrInvalidResetToken
	}

	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("reset password: hash password: %w", err)
	}
	if err := s.repo.UpdatePassword(ctx, reset.UserID, hash); err != nil {
		return fmt.Errorf("reset password: store password: %w", err)
	}
	if err := s.resets.ConsumeReset(ctx, reset.UserID, now); err != nil {
		return fmt.Errorf("reset password: consume token: %w", err)
	}

	// Receiving the email proves the address belongs to them, so there is
	// nothing left to verify.
	if _, err := s.repo.MarkEmailVerified(ctx, reset.UserID, now); err != nil {
		s.logger.WarnContext(ctx, "could not mark the email verified after a reset",
			slog.Int64("user_id", reset.UserID.Int64()), slog.Any("error", err))
	}

	s.logger.InfoContext(ctx, "password reset", slog.Int64("user_id", reset.UserID.Int64()))
	return nil
}
