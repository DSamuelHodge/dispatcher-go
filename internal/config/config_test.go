package config_test

import (
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/config"
)

func TestDefaultValid(t *testing.T) {
	if err := config.Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectZeroTimeout(t *testing.T) {
	d := config.Default()
	d.TaskTimeoutS = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error")
	}
}
