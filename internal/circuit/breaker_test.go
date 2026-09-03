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
