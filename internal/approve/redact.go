package approve

import (
	"fmt"
	"strings"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// RedactedMarker is the only allowed stand-in for secrets.
const RedactedMarker = "[REDACTED]"

// SecretFields returns arg names that must never appear in cleartext sinks.
func SecretFields(v verbs.Verb) map[string]struct{} {
	m := map[string]struct{}{}
	if v.StdinArg != nil && v.StdinArg.Arg != "" {
		m[v.StdinArg.Arg] = struct{}{}
	}
	return m
}

// RedactArgs copies args with secret fields replaced.
func RedactArgs(v verbs.Verb, args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	secrets := SecretFields(v)
	for k, val := range args {
		if _, sec := secrets[k]; sec {
			out[k] = RedactedMarker
			continue
		}
		out[k] = val
	}
	return out
}

// DialogBody builds a redacted one-line summary for the confirm prompt.
func DialogBody(v verbs.Verb, argvRedacted []string) string {
	return fmt.Sprintf("%s %s", v.Name, strings.Join(argvRedacted, " "))
}

// DialogTitle is the confirm title.
func DialogTitle(verbName string) string {
	return fmt.Sprintf("Approve %s?", verbName)
}

// ContainsSecret reports whether haystack includes any secret cleartext values.
func ContainsSecret(haystack string, secrets ...string) bool {
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(haystack, s) {
			return true
		}
	}
	return false
}
