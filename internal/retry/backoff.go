// Package retry computes attempt backoff schedules.
package retry

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"time"
)

// DelayAfterFailure returns wait after failed attempt index n (0-based),
// delay = base * 2^n + jitter(<=maxJitter).
func DelayAfterFailure(base time.Duration, n int, maxJitter time.Duration) time.Duration {
	if n < 0 {
		n = 0
	}
	mult := math.Pow(2, float64(n))
	d := time.Duration(float64(base) * mult)
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
