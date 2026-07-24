package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync"
	"time"

	"dispatchlab/internal/store"
	"dispatchlab/internal/telemetry"
)

var (
	// ErrNoSession means the request carried no usable guest token.
	ErrNoSession = errors.New("no valid guest session")
	// ErrForbidden means the token is valid but does not own the thing it
	// asked for.
	ErrForbidden = errors.New("not your simulation")
	// ErrQuota means this session already holds as many runs as it may.
	ErrQuota = errors.New("session simulation quota reached")
)

const (
	// DefaultSessionTTL is how long a guest session lives without being used.
	// Every authenticated request extends it, so an active visitor is never
	// cut off mid-demo, while an abandoned session ages out within the hour.
	DefaultSessionTTL = time.Hour
	// DefaultRunTTL is how long an anonymous run's history is kept. Long
	// enough to reload the page and keep watching, short enough that the
	// event log of a public demo does not grow without bound.
	DefaultRunTTL = 2 * time.Hour
	// DefaultSessionQuota bounds concurrent runs per visitor.
	DefaultSessionQuota = 3
	// touchInterval avoids writing to the session row on every single
	// request; extending an hour-long TTL once a minute is plenty.
	touchInterval = time.Minute
)

// tokenBytes is the entropy behind a guest token. 32 bytes makes a token
// unguessable, which matters because the token is the only thing standing
// between one visitor's runs and another's.
const tokenBytes = 32

// Sessions issues and validates guest tokens, and enforces what a session is
// allowed to hold. With no store attached it keeps sessions in memory, so the
// demo still works against a database-less server.
type Sessions struct {
	store   store.Store
	metrics *telemetry.Metrics
	logger  *slog.Logger

	ttl    time.Duration
	runTTL time.Duration
	quota  int

	mu       sync.Mutex
	fallback map[string]store.GuestSession
	// touched tracks when each token's expiry was last extended.
	touched map[string]time.Time
}

// SessionsConfig configures session lifetimes and quotas. Zero values fall
// back to the defaults above.
type SessionsConfig struct {
	Store   store.Store
	Metrics *telemetry.Metrics
	Logger  *slog.Logger
	TTL     time.Duration
	RunTTL  time.Duration
	Quota   int
}

// NewSessions returns a session service.
func NewSessions(cfg SessionsConfig) *Sessions {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultSessionTTL
	}
	if cfg.RunTTL <= 0 {
		cfg.RunTTL = DefaultRunTTL
	}
	if cfg.Quota <= 0 {
		cfg.Quota = DefaultSessionQuota
	}
	return &Sessions{
		store:    cfg.Store,
		metrics:  cfg.Metrics,
		logger:   cfg.Logger,
		ttl:      cfg.TTL,
		runTTL:   cfg.RunTTL,
		quota:    cfg.Quota,
		fallback: make(map[string]store.GuestSession),
		touched:  make(map[string]time.Time),
	}
}

// Quota is the per-session concurrent run limit.
func (s *Sessions) Quota() int { return s.quota }

// RunTTL is how long an anonymous run is retained.
func (s *Sessions) RunTTL() time.Duration { return s.runTTL }

// Issue creates a new guest session and returns it.
func (s *Sessions) Issue(ctx context.Context) (store.GuestSession, error) {
	token, err := generateToken()
	if err != nil {
		return store.GuestSession{}, err
	}

	now := time.Now().UTC()
	session := store.GuestSession{
		Token:      token,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.ttl),
	}

	if s.store == nil {
		s.mu.Lock()
		s.fallback[token] = session
		s.mu.Unlock()
		return session, nil
	}

	if err := s.store.CreateGuestSession(ctx, session); err != nil {
		s.metrics.PersistenceErrors().Inc()
		return store.GuestSession{}, err
	}
	return session, nil
}

// Validate resolves a token to its session, refusing unknown or expired
// tokens. A valid token has its life extended, at most once a minute, so an
// active visitor's runs are never pruned out from under them.
func (s *Sessions) Validate(ctx context.Context, token string) (store.GuestSession, error) {
	if token == "" {
		return store.GuestSession{}, ErrNoSession
	}

	session, err := s.lookup(ctx, token)
	if err != nil {
		return store.GuestSession{}, err
	}

	now := time.Now().UTC()
	if session.Expired(now) {
		return store.GuestSession{}, ErrNoSession
	}

	s.extend(ctx, &session, now)
	return session, nil
}

func (s *Sessions) lookup(ctx context.Context, token string) (store.GuestSession, error) {
	if s.store == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		session, ok := s.fallback[token]
		if !ok {
			return store.GuestSession{}, ErrNoSession
		}
		return session, nil
	}

	session, err := s.store.GetGuestSession(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return store.GuestSession{}, ErrNoSession
	}
	if err != nil {
		return store.GuestSession{}, err
	}
	return session, nil
}

func (s *Sessions) extend(ctx context.Context, session *store.GuestSession, now time.Time) {
	s.mu.Lock()
	last := s.touched[session.Token]
	if now.Sub(last) < touchInterval {
		s.mu.Unlock()
		return
	}
	s.touched[session.Token] = now
	s.mu.Unlock()

	expiresAt := now.Add(s.ttl)
	session.LastSeenAt = now
	session.ExpiresAt = expiresAt

	if s.store == nil {
		s.mu.Lock()
		s.fallback[session.Token] = *session
		s.mu.Unlock()
		return
	}

	if err := s.store.TouchGuestSession(ctx, session.Token, now, expiresAt); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.metrics.PersistenceErrors().Inc()
		s.logger.Warn("could not extend a guest session", "error", err)
	}
}

// CheckQuota reports whether a session may create another run. held is how
// many this process currently holds for the token; the store is consulted too
// so runs recorded by another instance still count.
func (s *Sessions) CheckQuota(ctx context.Context, token string, held int) error {
	if held >= s.quota {
		return ErrQuota
	}
	if s.store == nil {
		return nil
	}

	stored, err := s.store.CountSimulationsForToken(ctx, token)
	if err != nil {
		// a quota check that cannot reach the database must not lock a
		// visitor out; the in-process count above is still enforced.
		s.logger.Warn("could not count a session's runs, allowing on the in-process count", "error", err)
		return nil
	}
	if stored >= s.quota {
		return ErrQuota
	}
	return nil
}

// Forget drops a session from the in-memory fallback. Used by tests and by
// the retention sweep when there is no store to do it for us.
func (s *Sessions) Forget(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fallback, token)
	delete(s.touched, token)
}

// PurgeFallback removes expired sessions from the in-memory map, which is the
// database-less equivalent of the store's retention sweep.
func (s *Sessions) PurgeFallback(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for token, session := range s.fallback {
		if session.Expired(now) {
			delete(s.fallback, token)
			delete(s.touched, token)
			removed++
		}
	}
	return removed
}

func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
