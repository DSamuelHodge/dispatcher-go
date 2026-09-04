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

func TestValidateArgvEqualsFlagForm(t *testing.T) {
	// `-w=x` strips to the known `-w` flag, so it passes flag validation
	// (the mutating check lives in verbs tier validation).
	if err := termuxallow.ValidateArgv([]string{"termux-nfc", "-w=x"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateArgvEmptyTemplateToken(t *testing.T) {
	// "{{}}" is not a placeholder; as a positional it is still allowed,
	// so validation outcome is unchanged.
	if err := termuxallow.ValidateArgv([]string{"termux-toast", "{{}}"}); err != nil {
		t.Fatal(err)
	}
}
