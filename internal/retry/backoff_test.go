package retry_test

import (
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/retry"
)

func TestDelayGrows(t *testing.T) {
	base := time.Second
	d0 := retry.DelayAfterFailure(base, 0, 0)
	d1 := retry.DelayAfterFailure(base, 1, 0)
	d2 := retry.DelayAfterFailure(base, 2, 0)
	if d0 != time.Second || d1 != 2*time.Second || d2 != 4*time.Second {
		t.Fatalf("%v %v %v", d0, d1, d2)
	}
}

func TestShouldExhaust(t *testing.T) {
	if retry.ShouldExhaust(4, 5) {
		t.Fatal("attempt 4 of max 5 should retry")
	}
	if !retry.ShouldExhaust(5, 5) {
		t.Fatal("attempt 5 should exhaust")
	}
}

func TestLargeNSaturates(t *testing.T) {
	for _, n := range []int{30, 62, 100, 1 << 30} {
		if d := retry.DelayAfterFailure(time.Second, n, 0); d != retry.MaxDelay {
			t.Fatalf("n=%d: got %v, want %v", n, d, retry.MaxDelay)
		}
	}
	if d := retry.DelayAfterFailure(time.Minute, 10, 0); d != retry.MaxDelay {
		t.Fatalf("large base: got %v, want %v", d, retry.MaxDelay)
	}
	// Capped variant honours a custom cap.
	if d := retry.DelayAfterFailureCapped(time.Second, 10, 0, 10*time.Second); d != 10*time.Second {
		t.Fatalf("custom cap: got %v", d)
	}
	// Non-positive custom cap falls back to MaxDelay.
	if d := retry.DelayAfterFailureCapped(time.Second, 100, 0, 0); d != retry.MaxDelay {
		t.Fatalf("fallback cap: got %v", d)
	}
}

func TestNegativeInputsClamped(t *testing.T) {
	if d := retry.DelayAfterFailure(time.Second, -5, 0); d != time.Second {
		t.Fatalf("negative n: got %v", d)
	}
	if d := retry.DelayAfterFailure(-time.Second, 2, 0); d != 0 {
		t.Fatalf("negative base: got %v", d)
	}
	if d := retry.DelayAfterFailure(time.Second, 1, -time.Second); d != 2*time.Second {
		t.Fatalf("negative jitter: got %v", d)
	}
	if d := retry.DelayAfterFailure(0, 5, 0); d != 0 {
		t.Fatalf("zero base: got %v", d)
	}
}

func TestJitterBoundRespected(t *testing.T) {
	base := time.Second
	const n = 2 // 4s exponential
	const maxJitter = 100 * time.Millisecond
	for i := 0; i < 200; i++ {
		d := retry.DelayAfterFailure(base, n, maxJitter)
		if d < 4*time.Second || d >= 4*time.Second+maxJitter {
			t.Fatalf("iter %d: %v outside [4s, 4.1s)", i, d)
		}
	}
	// Saturated value plus jitter stays within [MaxDelay, MaxDelay+jitter).
	for i := 0; i < 50; i++ {
		d := retry.DelayAfterFailure(time.Second, 100, maxJitter)
		if d < retry.MaxDelay || d >= retry.MaxDelay+maxJitter {
			t.Fatalf("iter %d: %v outside saturated jitter bound", i, d)
		}
	}
}
