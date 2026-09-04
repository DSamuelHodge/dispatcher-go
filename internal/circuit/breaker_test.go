package circuit_test

import (
	"testing"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
)

func TestTripsAfterThreshold(t *testing.T) {
	b := circuit.New(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !b.Allow() {
			t.Fatalf("allow before trip at %d", i)
		}
		b.Failure()
	}
	if b.Allow() {
		t.Fatal("should be open")
	}
	if b.Snapshot().State != circuit.Open {
		t.Fatalf("%v", b.Snapshot())
	}
}

func TestHalfOpenRecover(t *testing.T) {
	b := circuit.New(2, 20*time.Millisecond)
	b.Failure()
	b.Failure()
	if b.Allow() {
		t.Fatal("open")
	}
	time.Sleep(25 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("half-open probe should allow")
	}
	if b.Allow() {
		t.Fatal("second probe blocked")
	}
	b.Success()
	if b.Snapshot().State != circuit.Closed {
		t.Fatal("should close")
	}
	if !b.Allow() {
		t.Fatal("closed allows")
	}
}

func TestHalfOpenFailReopens(t *testing.T) {
	b := circuit.New(1, 10*time.Millisecond)
	b.Failure()
	time.Sleep(15 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("probe")
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("re-open")
	}
}

func TestTripAfterFive(t *testing.T) {
	now := time.Now()
	b := circuit.New(5, time.Minute)
	b.SetNowFunc(func() time.Time { return now })
	for i := 0; i < 4; i++ {
		if !b.Allow() {
			t.Fatalf("allow before trip at %d", i)
		}
		b.Failure()
	}
	if !b.Allow() {
		t.Fatal("4 failures should not trip a threshold-5 breaker")
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("5 failures should trip")
	}
	if b.Snapshot().State != circuit.Open {
		t.Fatalf("state=%v", b.Snapshot())
	}
}

func TestSingleProbe(t *testing.T) {
	now := time.Now()
	b := circuit.New(1, time.Minute)
	b.SetNowFunc(func() time.Time { return now })
	b.Failure()
	now = now.Add(2 * time.Minute) // past open duration
	if !b.Allow() {
		t.Fatal("half-open probe should allow")
	}
	if b.Allow() {
		t.Fatal("second probe while one is outstanding must block")
	}
}

func TestProbeFailReopens(t *testing.T) {
	now := time.Now()
	b := circuit.New(1, time.Minute)
	b.SetNowFunc(func() time.Time { return now })
	b.Failure()
	now = now.Add(2 * time.Minute)
	if !b.Allow() {
		t.Fatal("probe should allow")
	}
	b.Failure() // probe verdict: fail → reopen
	if b.Allow() {
		t.Fatal("breaker should be open after probe failure")
	}
	if b.Snapshot().State != circuit.Open {
		t.Fatalf("state=%v", b.Snapshot())
	}
	now = now.Add(2 * time.Minute)
	if !b.Allow() {
		t.Fatal("new probe should allow after reopen duration elapses")
	}
}

func TestProbeWedgeExpiry(t *testing.T) {
	now := time.Now()
	b := circuit.New(1, time.Minute) // default probe lease: 2x open = 2m
	b.SetNowFunc(func() time.Time { return now })
	b.Failure()
	now = now.Add(61 * time.Second) // open → half-open
	if !b.Allow() {
		t.Fatal("first probe should allow")
	}
	if b.Allow() {
		t.Fatal("outstanding probe must block a second probe")
	}
	// The probe verdict never arrives; advance past the lease without
	// recording Success/Failure. Allow must permit a fresh probe instead
	// of wedging forever.
	now = now.Add(2*time.Minute + time.Second)
	if !b.Allow() {
		t.Fatal("expired probe lease should permit a new probe")
	}
	if b.Allow() {
		t.Fatal("fresh probe is now outstanding; second must block")
	}
	// And the fresh probe can still close the breaker on success.
	b.Success()
	if b.Snapshot().State != circuit.Closed {
		t.Fatalf("state=%v", b.Snapshot())
	}
}

func TestProbeTimeoutField(t *testing.T) {
	now := time.Now()
	b := circuit.New(1, time.Hour)
	b.ProbeTimeout = 50 * time.Millisecond
	b.SetNowFunc(func() time.Time { return now })
	b.Failure()
	now = now.Add(2 * time.Hour) // open → half-open
	if !b.Allow() {
		t.Fatal("first probe should allow")
	}
	now = now.Add(51 * time.Millisecond) // past custom lease, well under OpenFor
	if !b.Allow() {
		t.Fatal("custom probe timeout should permit a new probe")
	}
}
