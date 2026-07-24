package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// atRate returns a limiter whose clock the test drives directly, so refill
// behaviour is checked without sleeping through it.
func atRate(t *testing.T, cfg Config) (*Limiter, func(time.Duration)) {
	t.Helper()
	l := New(cfg)
	now := time.Now()
	l.now = func() time.Time { return now }
	return l, func(d time.Duration) { now = now.Add(d) }
}

func TestBurstThenRefill(t *testing.T) {
	l, advance := atRate(t, Config{PerSecond: 2, Burst: 3})

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("request %d within the burst was refused", i+1)
		}
	}

	ok, retry := l.Allow("k")
	if ok {
		t.Fatal("a fourth request past the burst was allowed")
	}
	if retry <= 0 || retry > time.Second {
		t.Errorf("retry-after = %v, want a value inside a second", retry)
	}

	// half a second at two per second is one more token.
	advance(500 * time.Millisecond)
	if ok, _ := l.Allow("k"); !ok {
		t.Error("a token that should have refilled was not available")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Error("more refilled than the elapsed time allows")
	}
}

func TestTokensCapAtBurst(t *testing.T) {
	l, advance := atRate(t, Config{PerSecond: 1, Burst: 2})

	// idling far longer than it takes to refill must not bank extra tokens.
	advance(time.Hour)
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("request %d after a long idle was refused", i+1)
		}
	}
	if ok, _ := l.Allow("k"); ok {
		t.Error("an idle key banked more than its burst")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := atRate(t, Config{PerSecond: 1, Burst: 1})

	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("first key was refused")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("first key exceeded its burst")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("a second key was refused because of the first key's usage")
	}
}

// a stream of one-off keys must not grow the map forever.
func TestIdleBucketsAreSwept(t *testing.T) {
	l, advance := atRate(t, Config{PerSecond: 1, Burst: 1, IdleTTL: time.Minute})

	for i := 0; i < 50; i++ {
		l.Allow(string(rune('a' + i%26)))
	}
	if l.Len() == 0 {
		t.Fatal("no buckets were tracked")
	}

	advance(2 * time.Minute)
	l.Allow("fresh-key")

	if l.Len() != 1 {
		t.Errorf("after sweeping, %d buckets remain, want only the fresh one", l.Len())
	}
}

// a non-positive rate is how limiting is switched off, and every method has
// to tolerate the resulting nil.
func TestDisabledLimiterAllowsEverything(t *testing.T) {
	l := New(Config{PerSecond: 0})
	if l != nil {
		t.Fatal("a non-positive rate should produce a nil limiter")
	}
	for i := 0; i < 100; i++ {
		if ok, retry := l.Allow("k"); !ok || retry != 0 {
			t.Fatal("a disabled limiter refused a request")
		}
	}
	if l.Len() != 0 {
		t.Error("a disabled limiter reports tracked keys")
	}
}

func TestConcurrentAllowIsRaceFree(t *testing.T) {
	l := New(Config{PerSecond: 1000, Burst: 1000})

	var wg sync.WaitGroup
	allowed := make([]int, 8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if ok, _ := l.Allow("shared"); ok {
					allowed[w]++
				}
			}
		}(w)
	}
	wg.Wait()

	total := 0
	for _, n := range allowed {
		total += n
	}
	// 1600 attempts against a burst of 1000, with refill during the run: the
	// count must land above the burst floor and never exceed the attempts.
	if total > 1600 {
		t.Errorf("allowed %d of 1600 attempts", total)
	}
	if total < 1000 {
		t.Errorf("allowed only %d, want at least the burst of 1000", total)
	}
}
