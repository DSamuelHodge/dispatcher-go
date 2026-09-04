package approve_test

import (
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func TestRedactArgs(t *testing.T) {
	v := verbs.Verb{Name: "sms.send", StdinArg: &verbs.StdinArg{Arg: "text"}}
	out := approve.RedactArgs(v, map[string]any{"number": "1", "text": "secret"})
	if out["text"] != approve.RedactedMarker {
		t.Fatalf("%v", out)
	}
	if out["number"] != "1" {
		t.Fatalf("%v", out)
	}
}

func TestContainsSecret(t *testing.T) {
	if !approve.ContainsSecret("hello secret world", "secret") {
		t.Fatal("expected hit")
	}
	if approve.ContainsSecret("hello", "secret") {
		t.Fatal("unexpected hit")
	}
}
