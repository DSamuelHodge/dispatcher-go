package termuxallow_test

import (
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/termuxallow"
)

func TestValidateArgvOK(t *testing.T) {
	if err := termuxallow.ValidateArgv([]string{"termux-battery-status"}); err != nil {
		t.Fatal(err)
	}
	if err := termuxallow.ValidateArgv([]string{"termux-sms-send", "-n", "555", "{{.text}}"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateArgvUnknown(t *testing.T) {
	if err := termuxallow.ValidateArgv([]string{"curl"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestIsMutating(t *testing.T) {
	if termuxallow.IsMutating("termux-battery-status") {
		t.Fatal("battery should be non-mutating")
	}
	if !termuxallow.IsMutating("termux-sms-send") {
		t.Fatal("sms-send should be mutating")
	}
}
