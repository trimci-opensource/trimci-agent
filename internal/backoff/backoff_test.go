package backoff

import (
	"context"
	"testing"
	"time"
)

func TestNextBounds(t *testing.T) {
	var b Backoff
	for i := 0; i < 100; i++ {
		d := b.Next()
		if d < time.Second || d > Cap {
			t.Fatalf("attempt %d: delay %v outside [1s, %v]", i, d, Cap)
		}
	}
}

func TestResetShrinksCeiling(t *testing.T) {
	var b Backoff
	for i := 0; i < 10; i++ {
		b.Next()
	}
	b.Reset()
	// After reset the ceiling is Base again; sample a few draws.
	for i := 0; i < 20; i++ {
		if d := (&b).Next(); d > Base && i == 0 {
			t.Fatalf("first post-reset delay %v exceeds base ceiling %v", d, Base)
		}
		b.Reset()
	}
}

func TestSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); err == nil {
		t.Fatal("expected context error")
	}
	if err := Sleep(context.Background(), 0); err != nil {
		t.Fatalf("zero sleep: %v", err)
	}
}
