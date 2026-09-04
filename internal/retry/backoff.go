// Package retry computes attempt backoff schedules.
package retry

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// MaxDelay caps the exponential component of DelayAfterFailure so very
// large attempt indexes (or large bases) can't overflow into absurd or
// negative durations. Jitter, when configured, is added on top of the
// capped value.
const MaxDelay = 5 * time.Minute

// DelayAfterFailure returns wait after failed attempt index n (0-based),
// delay = min(base * 2^n, MaxDelay) + jitter(<=maxJitter).
// Negative inputs are clamped to sane values (n→0, base/jitter→0).
func DelayAfterFailure(base time.Duration, n int, maxJitter time.Duration) time.Duration {
	return DelayAfterFailureCapped(base, n, maxJitter, MaxDelay)
}

// DelayAfterFailureCapped is DelayAfterFailure with an explicit cap on the
// exponential component. Non-positive maxDelay falls back to MaxDelay.
func DelayAfterFailureCapped(base time.Duration, n int, maxJitter, maxDelay time.Duration) time.Duration {
	if n < 0 {
		n = 0
	}
	if base < 0 {
		base = 0
	}
	if maxJitter < 0 {
		maxJitter = 0
	}
	if maxDelay <= 0 {
		maxDelay = MaxDelay
	}
	// Saturating doubling: stops at maxDelay instead of overflowing.
	d := base
	for i := 0; i < n; i++ {
		if d >= maxDelay {
			d = maxDelay
			break
		}
		d *= 2
		if d < 0 || d >= maxDelay {
			// d < 0 catches int64 wraparound on huge shifts.
			// (Zero stays zero: 0*2 == 0 is a legitimate delay.)
			d = maxDelay
			break
		}
	}
	if d > maxDelay {
		d = maxDelay
	}
	if maxJitter > 0 {
		d += jitter(maxJitter)
	}
	return d
}

// ShouldExhaust reports whether attempt index n (just failed) is the last.
// Attempts run 0..maxRetries inclusive.
func ShouldExhaust(n, maxRetries int) bool {
	return n >= maxRetries
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	// #nosec G404 — jitter only
	v := binary.LittleEndian.Uint64(b[:])
	return time.Duration(v % uint64(max))
}
