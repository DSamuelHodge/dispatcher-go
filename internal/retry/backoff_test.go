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
