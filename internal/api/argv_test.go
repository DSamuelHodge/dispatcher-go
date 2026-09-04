package api

import (
	"reflect"
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func locOnceVerb() verbs.Verb {
	return verbs.Verb{
		Name: "location.once",
		Argv: []string{"termux-location", "-p", "{{.provider}}", "-r", "{{.request}}"},
		Args: []verbs.ArgSpec{
			{Name: "provider", Flag: "-p", Type: "string", Required: false},
			{Name: "request", Flag: "-r", Type: "string", Required: false},
		},
	}
}

func TestExpandArgvOmitsEmptyOptionalPairs(t *testing.T) {
	argv, _, err := expandArgv(locOnceVerb(), map[string]any{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"termux-location"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("omitted optionals: got %q want %q", argv, want)
	}
}

func TestExpandArgvKeepsProvidedOptional(t *testing.T) {
	argv, _, err := expandArgv(locOnceVerb(), map[string]any{"provider": "gps"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"termux-location", "-p", "gps"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("got %q want %q", argv, want)
	}
}

func TestExpandArgvRequiredStillEnforced(t *testing.T) {
	v := verbs.Verb{
		Name: "job.schedule",
		Argv: []string{"termux-job-scheduler", "-j", "{{.job_id}}"},
		Args: []verbs.ArgSpec{
			{Name: "job_id", Flag: "-j", Type: "string", Required: true},
		},
	}
	if _, _, err := expandArgv(v, map[string]any{}, ""); err == nil {
		t.Fatal("expected missing-required error")
	}
	argv, _, err := expandArgv(v, map[string]any{"job_id": "7"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"termux-job-scheduler", "-j", "7"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("got %q want %q", argv, want)
	}
}
