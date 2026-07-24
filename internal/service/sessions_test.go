package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"dispatchlab/internal/store"
)

func TestIssuedTokensAreUniqueAndUnguessable(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{Store: store.NewMemory()})

	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		session, err := sessions.Issue(ctx)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if seen[session.Token] {
			t.Fatal("Issue returned a duplicate token")
		}
		seen[session.Token] = true

		// 32 random bytes, base64url without padding.
		if len(session.Token) < 40 {
			t.Fatalf("token %q is too short to be unguessable", session.Token)
		}
	}
}

func TestValidateAcceptsIssuedTokens(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{Store: store.NewMemory()})

	issued, err := sessions.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := sessions.Validate(ctx, issued.Token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Token != issued.Token {
		t.Errorf("token = %q, want %q", got.Token, issued.Token)
	}
}

func TestValidateRejectsUnknownAndEmptyTokens(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{Store: store.NewMemory()})

	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := sessions.Validate(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Errorf("Validate(%q) error = %v, want ErrNoSession", token, err)
		}
	}
}

func TestValidateRejectsExpiredTokens(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	sessions := NewSessions(SessionsConfig{Store: s})

	expired := store.GuestSession{
		Token:      "expired-token",
		CreatedAt:  time.Now().Add(-2 * time.Hour),
		LastSeenAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt:  time.Now().Add(-time.Hour),
	}
	if err := s.CreateGuestSession(ctx, expired); err != nil {
		t.Fatalf("CreateGuestSession: %v", err)
	}

	if _, err := sessions.Validate(ctx, expired.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Validate on an expired token = %v, want ErrNoSession", err)
	}
}

// an active visitor must not have their session age out mid-demo.
func TestValidateExtendsAnActiveSession(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	sessions := NewSessions(SessionsConfig{Store: s, TTL: time.Hour})

	issued, err := sessions.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// backdate the stored expiry, then use the session; validating has to
	// push it back out to a full TTL from now.
	near := time.Now().UTC().Add(time.Minute)
	if err := s.TouchGuestSession(ctx, issued.Token, time.Now().UTC(), near); err != nil {
		t.Fatalf("TouchGuestSession: %v", err)
	}

	if _, err := sessions.Validate(ctx, issued.Token); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got, err := s.GetGuestSession(ctx, issued.Token)
	if err != nil {
		t.Fatalf("GetGuestSession: %v", err)
	}
	if !got.ExpiresAt.After(near) {
		t.Errorf("expiry was not extended: %v is not after %v", got.ExpiresAt, near)
	}
}

func TestQuotaCountsHeldAndStoredRuns(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	sessions := NewSessions(SessionsConfig{Store: s, Quota: 2})

	issued, err := sessions.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := sessions.CheckQuota(ctx, issued.Token, 0); err != nil {
		t.Fatalf("a session with no runs was refused: %v", err)
	}
	if err := sessions.CheckQuota(ctx, issued.Token, 2); !errors.Is(err, ErrQuota) {
		t.Errorf("in-process count at quota = %v, want ErrQuota", err)
	}

	// runs recorded in the store count too, even if this process holds none.
	for i, id := range []string{"sim-a", "sim-b"} {
		sim := store.Simulation{ID: id, Seed: int64(i), Drivers: 4, GuestToken: issued.Token}
		if err := s.CreateSimulation(ctx, sim); err != nil {
			t.Fatalf("CreateSimulation: %v", err)
		}
	}
	if err := sessions.CheckQuota(ctx, issued.Token, 0); !errors.Is(err, ErrQuota) {
		t.Errorf("stored runs did not count toward the quota: %v", err)
	}
}

// with no database the service still has to issue and validate tokens, since
// that is how the demo runs locally.
func TestSessionsWorkWithoutAStore(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{})

	issued, err := sessions.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := sessions.Validate(ctx, issued.Token); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := sessions.CheckQuota(ctx, issued.Token, 0); err != nil {
		t.Fatalf("CheckQuota: %v", err)
	}
	if err := sessions.CheckQuota(ctx, issued.Token, 99); !errors.Is(err, ErrQuota) {
		t.Errorf("quota is not enforced without a store: %v", err)
	}

	sessions.Forget(issued.Token)
	if _, err := sessions.Validate(ctx, issued.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("a forgotten token still validates: %v", err)
	}
}

func TestPurgeFallbackDropsExpiredSessions(t *testing.T) {
	ctx := context.Background()
	sessions := NewSessions(SessionsConfig{TTL: time.Hour})

	issued, err := sessions.Issue(ctx)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if removed := sessions.PurgeFallback(time.Now()); removed != 0 {
		t.Errorf("purged %d live sessions", removed)
	}
	if removed := sessions.PurgeFallback(time.Now().Add(2 * time.Hour)); removed != 1 {
		t.Errorf("purged %d expired sessions, want 1", removed)
	}
	if _, err := sessions.Validate(ctx, issued.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("a purged token still validates: %v", err)
	}
}
