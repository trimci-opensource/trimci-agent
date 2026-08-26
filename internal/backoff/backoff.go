// Package backoff implements the agent's retry pacing rules (PROTOCOL.md):
// exponential backoff with full jitter, base 5 s, cap 15 min, for connection
// errors and 5xx responses. Rate limits (429) are NOT backed off — their
// Retry-After value is honored exactly by the callers.
package backoff

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	Base = 5 * time.Second
	Cap  = 15 * time.Minute
)

// Sleeper waits for d or until ctx is done (returning ctx.Err()). Injected
// everywhere the agent sleeps so tests run without real time passing.
type Sleeper func(ctx context.Context, d time.Duration) error

// Sleep is the production Sleeper.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Backoff tracks consecutive failures and yields the next jittered delay.
type Backoff struct {
	attempts int
}

// Next returns the delay before the next retry: full jitter over
// min(Base·2^attempts, Cap), never below one second so a hot failure loop
// cannot spin.
func (b *Backoff) Next() time.Duration {
	ceiling := Base << b.attempts
	if ceiling > Cap || ceiling <= 0 { // <= 0 guards shift overflow
		ceiling = Cap
	}
	if b.attempts < 63 {
		b.attempts++
	}
	d := time.Duration(rand.Int64N(int64(ceiling)))
	if d < time.Second {
		d = time.Second
	}
	return d
}

// Reset clears the failure streak after a success.
func (b *Backoff) Reset() { b.attempts = 0 }
