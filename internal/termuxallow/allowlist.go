// Package termuxallow is a checked-in snapshot of termux-api-package script
// names (and curated flags) used for load-time verb catalog validation.
//
// Source: termux/termux-api-package scripts/*.in (API app surface ~v0.53.0).
// termux-open is from termux-tools (not api-package) and is allowlisted
// explicitly for open.url per spec §5.2.
//
// Update policy: when bumping the Termux:API target, refresh Binaries and
// Flags from upstream .in scripts and extend tests.
package termuxallow

import "fmt"

// Binary describes one allowlisted executable.
type Binary struct {
	// Name is the argv0 base name (e.g. "termux-battery-status").
	Name string
	// Mutating is true if the binary can change device state. Tier A verbs
	// MUST NOT reference mutating binaries (FR-1.2).
	Mutating bool
	// Flags are allowed argv tokens that start with '-'. Positional args and
	// {{.template}} placeholders are not listed here. Empty means the binary
	// takes no dash-flags (positionals only).
	Flags map[string]struct{}
}

func flags(list ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(list))
	for _, f := range list {
		m[f] = struct{}{}
	}
	return m
}

// Binaries is the normative allowlist keyed by argv0 base name.
var Binaries = map[string]Binary{
	// --- Tier A / read-oriented ---
	"termux-audio-info":           {Name: "termux-audio-info", Mutating: false, Flags: flags()},
	"termux-battery-status":       {Name: "termux-battery-status", Mutating: false, Flags: flags()},
	"termux-call-log":             {Name: "termux-call-log", Mutating: false, Flags: flags("-l", "-o")},
	"termux-camera-info":          {Name: "termux-camera-info", Mutating: false, Flags: flags()},
	"termux-clipboard-get":        {Name: "termux-clipboard-get", Mutating: false, Flags: flags()},
	"termux-contact-list":         {Name: "termux-contact-list", Mutating: false, Flags: flags()},
	"termux-infrared-frequencies": {Name: "termux-infrared-frequencies", Mutating: false, Flags: flags()},
	"termux-location":             {Name: "termux-location", Mutating: false, Flags: flags("-p", "-r")},
	"termux-notification-list":    {Name: "termux-notification-list", Mutating: false, Flags: flags()},
	"termux-saf-dirs":             {Name: "termux-saf-dirs", Mutating: false, Flags: flags()},
	"termux-saf-ls":               {Name: "termux-saf-ls", Mutating: false, Flags: flags("-u")},
	"termux-saf-read":             {Name: "termux-saf-read", Mutating: false, Flags: flags()},
	"termux-saf-stat":             {Name: "termux-saf-stat", Mutating: false, Flags: flags()},
	"termux-sensor":               {Name: "termux-sensor", Mutating: false, Flags: flags("-s", "-n", "-d", "-a", "-l", "-c")},
	"termux-sms-inbox":            {Name: "termux-sms-inbox", Mutating: false, Flags: flags("-d", "-l", "-n", "-o", "-t", "--version")},
	"termux-sms-list":             {Name: "termux-sms-list", Mutating: false, Flags: flags("-d", "-l", "-n", "-o", "-t", "--version")},
	"termux-telephony-cellinfo":   {Name: "termux-telephony-cellinfo", Mutating: false, Flags: flags()},
	"termux-telephony-deviceinfo": {Name: "termux-telephony-deviceinfo", Mutating: false, Flags: flags()},
	"termux-tts-engines":          {Name: "termux-tts-engines", Mutating: false, Flags: flags()},
	"termux-usb":                  {Name: "termux-usb", Mutating: false, Flags: flags("-l", "-r", "-e", "-E")},
	"termux-volume":               {Name: "termux-volume", Mutating: false, Flags: flags()}, // query form; set uses positionals
	"termux-wifi-connectioninfo":  {Name: "termux-wifi-connectioninfo", Mutating: false, Flags: flags()},
	"termux-wifi-scaninfo":        {Name: "termux-wifi-scaninfo", Mutating: false, Flags: flags()},
	"termux-nfc":                  {Name: "termux-nfc", Mutating: false, Flags: flags("-r", "-w", "-t", "-n")}, // -w write is mutating; see ValidateArgv

	// --- Tier B / mutating ---
	"termux-brightness":           {Name: "termux-brightness", Mutating: true, Flags: flags()},
	"termux-camera-photo":         {Name: "termux-camera-photo", Mutating: true, Flags: flags("-c", "-f", "-p", "-i")},
	"termux-clipboard-set":        {Name: "termux-clipboard-set", Mutating: true, Flags: flags()},
	"termux-dialog":               {Name: "termux-dialog", Mutating: true, Flags: flags("-i", "-m", "-p", "-n", "-t", "-d", "-v", "-r")},
	"termux-download":             {Name: "termux-download", Mutating: true, Flags: flags("-d", "-p", "-t")},
	"termux-fingerprint":          {Name: "termux-fingerprint", Mutating: true, Flags: flags("-t", "-d", "-s", "-c")},
	"termux-infrared-transmit":    {Name: "termux-infrared-transmit", Mutating: true, Flags: flags("-f")},
	"termux-job-scheduler":        {Name: "termux-job-scheduler", Mutating: true, Flags: flags("-s", "-j", "--job-id", "--period-ms", "--network", "--battery-not-low", "--charging", "--persisted", "--storage-not-low", "--trigger-content-uri", "--trigger-content-flag", "--cancel", "--cancel-all")},
	"termux-keystore":             {Name: "termux-keystore", Mutating: true, Flags: flags("-a", "-d", "-f", "-u", "-i", "-p", "-v", "-s", "-1")},
	"termux-media-player":         {Name: "termux-media-player", Mutating: true, Flags: flags("-f")},
	"termux-media-scan":           {Name: "termux-media-scan", Mutating: true, Flags: flags("-r", "-v")},
	"termux-microphone-record":    {Name: "termux-microphone-record", Mutating: true, Flags: flags("-d", "-f", "-l", "-i", "-c", "-q")},
	"termux-notification":         {Name: "termux-notification", Mutating: true, Flags: flags("-c", "-t", "-i", "-id", "--id", "-u", "--ongoing", "--priority", "--sound", "--vibrate", "--channel", "--group", "--icon", "--image-path", "--action", "--on-delete", "--button1", "--button1-action", "--button2", "--button2-action", "--button3", "--button3-action", "--led-color", "--led-on", "--led-off", "--alert-once", "--type", "--media-play", "--media-pause", "--media-next", "--media-previous", "--help-actions", "-n")},
	"termux-notification-channel": {Name: "termux-notification-channel", Mutating: true, Flags: flags("-d", "-i", "-n", "-s", "-v", "-l", "-b", "-c", "-g", "-p", "-t", "-u", "-m", "-y")},
	"termux-notification-remove":  {Name: "termux-notification-remove", Mutating: true, Flags: flags("-i", "--id")},
	"termux-saf-create":           {Name: "termux-saf-create", Mutating: true, Flags: flags("-u", "-t", "-n", "-m")},
	"termux-saf-managedir":        {Name: "termux-saf-managedir", Mutating: true, Flags: flags()},
	"termux-saf-mkdir":            {Name: "termux-saf-mkdir", Mutating: true, Flags: flags("-u", "-n")},
	"termux-saf-rm":               {Name: "termux-saf-rm", Mutating: true, Flags: flags("-u")},
	"termux-saf-write":            {Name: "termux-saf-write", Mutating: true, Flags: flags("-u")},
	"termux-share":                {Name: "termux-share", Mutating: true, Flags: flags("-a", "-c", "-d", "-t", "-e")},
	"termux-sms-send":             {Name: "termux-sms-send", Mutating: true, Flags: flags("-n", "-s")},
	"termux-speech-to-text":       {Name: "termux-speech-to-text", Mutating: true, Flags: flags()},
	"termux-storage-get":          {Name: "termux-storage-get", Mutating: true, Flags: flags("-f")},
	"termux-telephony-call":       {Name: "termux-telephony-call", Mutating: true, Flags: flags()},
	"termux-toast":                {Name: "termux-toast", Mutating: true, Flags: flags("-b", "-c", "-g", "-s", "-t")},
	"termux-torch":                {Name: "termux-torch", Mutating: true, Flags: flags()},
	"termux-tts-speak":            {Name: "termux-tts-speak", Mutating: true, Flags: flags("-e", "-l", "-n", "-p", "-r", "-s", "-t", "-v", "--engine", "--language", "--pitch", "--rate", "--stream")},
	"termux-vibrate":              {Name: "termux-vibrate", Mutating: true, Flags: flags("-d", "-f")},
	"termux-wallpaper":            {Name: "termux-wallpaper", Mutating: true, Flags: flags("-f", "-l", "--file", "--lockscreen")},
	"termux-wifi-enable":          {Name: "termux-wifi-enable", Mutating: true, Flags: flags()},
	// termux-tools (not api-package) — allowlisted for open.url only.
	"termux-open": {Name: "termux-open", Mutating: true, Flags: flags("-a", "-c", "-d", "--chooser", "--content-type", "--send")},
}

// Lookup returns the allowlist entry for argv0.
func Lookup(argv0 string) (Binary, bool) {
	b, ok := Binaries[argv0]
	return b, ok
}

// ValidateArgv checks argv[0] is allowlisted and every static dash-flag is known.
// Tokens matching {{...}} are template placeholders and are skipped.
// positionals and non-dash tokens are allowed without further checks here.
func ValidateArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("argv must be non-empty")
	}
	argv0 := argv[0]
	b, ok := Lookup(argv0)
	if !ok {
		return fmt.Errorf("unknown binary %q: not in termux allowlist", argv0)
	}
	for i := 1; i < len(argv); i++ {
		tok := argv[i]
		if isTemplate(tok) {
			continue
		}
		if len(tok) > 0 && tok[0] == '-' {
			// Flags may be combined with values as separate tokens; only the
			// flag token itself is validated.
			flag := tok
			if eq := indexByte(tok, '='); eq > 0 {
				flag = tok[:eq]
			}
			if _, allowed := b.Flags[flag]; !allowed {
				return fmt.Errorf("binary %q: unknown flag %q", argv0, flag)
			}
		}
	}
	return nil
}

// IsMutating reports whether argv0 is classified as mutating.
func IsMutating(argv0 string) bool {
	b, ok := Lookup(argv0)
	if !ok {
		return true // unknown treated as mutating/unsafe
	}
	return b.Mutating
}

func isTemplate(s string) bool {
	// Require a non-empty template name: "{{}}" is not a placeholder.
	// Safe: as a positional it is still allowed, so no validation outcome changes.
	return len(s) >= 5 && s[0] == '{' && s[1] == '{' && s[len(s)-2] == '}' && s[len(s)-1] == '}'
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
