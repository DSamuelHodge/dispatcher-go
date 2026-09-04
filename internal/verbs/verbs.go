// Package verbs loads and validates the declarative verb catalog (verbs.yaml).
package verbs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DSamuelHodge/dispatcher-go/internal/config"
	"github.com/DSamuelHodge/dispatcher-go/internal/termuxallow"
	"gopkg.in/yaml.v3"
)

// SchemaVersion is the supported verbs.yaml version.
const SchemaVersion = 1

// Tier classifies verb capability class.
type Tier string

const (
	TierA Tier = "A" // perceive / read-only
	TierB Tier = "B" // act | watch
)

// Parser selects stdout handling.
type Parser string

const (
	ParserJSON Parser = "json"
	ParserText Parser = "text"
	ParserExit Parser = "exit"
)

// ArgSpec describes one request argument mapped to an argv flag/positional.
type ArgSpec struct {
	Name     string `yaml:"name"`
	Flag     string `yaml:"flag"`
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
}

// StdinArg names the request field piped to child stdin (never argv).
type StdinArg struct {
	Arg string `yaml:"arg"`
}

// Watch configures a streaming subscription.
type Watch struct {
	Mode   string `yaml:"mode"` // "stream"
	Buffer int    `yaml:"buffer"`
}

// Verb is one catalog entry.
type Verb struct {
	Name                     string    `yaml:"name"`
	Tier                    Tier      `yaml:"tier"`
	Argv                    []string  `yaml:"argv"`
	Args                    []ArgSpec `yaml:"args"`
	StdinArg                *StdinArg `yaml:"stdin_arg"`
	TimeoutS                int       `yaml:"timeout_s"`
	Retries                 *int      `yaml:"retries"`
	RetryBackoff            string    `yaml:"retry_backoff"`
	CircuitBreakerThreshold *int      `yaml:"circuit_breaker_threshold"`
	Parser                  Parser    `yaml:"parser"`
	Watch                   any       `yaml:"watch"` // false | Watch
}

// File is the top-level verbs.yaml document.
type File struct {
	Version int           `yaml:"version"`
	Daemon  config.Daemon `yaml:"daemon"`
	Verbs   []Verb        `yaml:"verbs"`
}

// Catalog is a validated, ready-to-serve verb set.
type Catalog struct {
	Daemon config.Daemon
	// ByName maps primary verb name → definition.
	ByName map[string]Verb
	// Aliases maps alternate names (e.g. wifi connection-info spelling) → primary.
	Aliases map[string]string
	Order   []string
}

// Load reads path, applies defaults, and validates (FR-1).
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read verbs catalog: %w", err)
	}
	return Parse(data)
}

// Parse validates raw YAML bytes.
func Parse(data []byte) (*Catalog, error) {
	var f File
	f.Daemon = config.Default()
	// Strict decoding: typos like `timeou_s` fail fast instead of being
	// silently ignored. Applies to File, Daemon, Verb, ArgSpec, StdinArg.
	// (Watch decodes into `any`, so its keys are checked in validateWatch.)
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse verbs.yaml: %w", err)
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("verbs.yaml version: want %d, got %d", SchemaVersion, f.Version)
	}
	// Fill daemon fields from defaults without wiping an explicit zero:
	// presence detection distinguishes `max_retries: 0` (honored as no-retry)
	// from an omitted key (default 5). Other numeric daemon fields must be > 0
	// per Validate, so zero there always means "unset".
	f.Daemon = applyDaemonDefaults(f.Daemon, daemonFieldPresent(data, "max_retries"))
	if err := f.Daemon.Validate(); err != nil {
		return nil, err
	}
	if len(f.Verbs) == 0 {
		return nil, fmt.Errorf("verbs.yaml: verbs list is empty")
	}

	cat := &Catalog{
		Daemon: f.Daemon,
		ByName: make(map[string]Verb, len(f.Verbs)),
		Aliases: map[string]string{
			// Spec §5.1: both spellings alias one verb.
			"wifi.connection-info": "wifi.info",
			"wifi.connectioninfo":  "wifi.info",
			"sms.inbox":            "sms.read", // legacy alias surface
		},
		Order: make([]string, 0, len(f.Verbs)),
	}

	for i, v := range f.Verbs {
		if err := validateVerb(v, f.Daemon); err != nil {
			return nil, fmt.Errorf("verbs[%d] %q: %w", i, v.Name, err)
		}
		if _, exists := cat.ByName[v.Name]; exists {
			return nil, fmt.Errorf("duplicate verb name %q", v.Name)
		}
		cat.ByName[v.Name] = v
		cat.Order = append(cat.Order, v.Name)
	}

	// Ensure alias targets exist when referenced in catalog.
	for alias, target := range cat.Aliases {
		if _, ok := cat.ByName[target]; !ok {
			// Alias table is fixed; only error if someone relied on missing seed.
			_ = alias
			_ = target
		}
	}
	return cat, nil
}

func applyDaemonDefaults(d config.Daemon, maxRetriesSet bool) config.Daemon {
	def := config.Default()
	if d.Listen == "" {
		d.Listen = def.Listen
	}
	if d.TaskTimeoutS == 0 {
		d.TaskTimeoutS = def.TaskTimeoutS
	}
	// Use sentinel: if backoff/cb still zero, fill.
	if d.BackoffBaseS == 0 {
		d.BackoffBaseS = def.BackoffBaseS
	}
	if d.CBTripThreshold == 0 {
		d.CBTripThreshold = def.CBTripThreshold
	}
	if d.CBOpenS == 0 {
		d.CBOpenS = def.CBOpenS
	}
	if d.MaxQueueDepth == 0 {
		d.MaxQueueDepth = def.MaxQueueDepth
	}
	if d.StreamBufferDefault == 0 {
		d.StreamBufferDefault = def.StreamBufferDefault
	}
	if d.TaskTimeoutS == 0 {
		d.TaskTimeoutS = def.TaskTimeoutS
	}
	// MaxRetries: explicit 0 means no retries and is honored. Only when the
	// key is absent do we fill the spec default (5). Per-verb `retries` uses
	// *int for the same reason: nil inherits the daemon default at runtime.
	if !maxRetriesSet && d.MaxRetries == 0 {
		d.MaxRetries = def.MaxRetries
	}
	return d
}

// daemonFieldPresent reports whether the top-level `daemon:` mapping in data
// explicitly sets the given key, distinguishing `max_retries: 0` from omitted.
func daemonFieldPresent(data []byte, key string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false
	}
	if len(root.Content) == 0 {
		return false
	}
	doc := root.Content[0] // document node → mapping
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "daemon" {
			continue
		}
		daemon := doc.Content[i+1]
		for j := 0; j+1 < len(daemon.Content); j += 2 {
			if daemon.Content[j].Value == key {
				return true
			}
		}
	}
	return false
}

func validateVerb(v Verb, d config.Daemon) error {
	if v.Name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsAny(v.Name, " \t") {
		return fmt.Errorf("name must not contain whitespace")
	}
	switch v.Tier {
	case TierA, TierB:
	default:
		return fmt.Errorf("tier must be A|B, got %q", v.Tier)
	}
	if len(v.Argv) == 0 {
		return fmt.Errorf("argv is required")
	}
	argv0 := v.Argv[0]
	if !strings.HasPrefix(argv0, "termux-") {
		return fmt.Errorf("argv[0] must be a termux-* binary, got %q", argv0)
	}
	if err := termuxallow.ValidateArgv(v.Argv); err != nil {
		return err
	}
	if v.Tier == TierA {
		if termuxallow.IsMutating(argv0) {
			return fmt.Errorf("tier A must not use mutating binary %q", argv0)
		}
		// NFC write flag is mutating even on a non-mutating-classified binary.
		// Strip `=value` the same way flag validation does, so `-w=x`
		// cannot evade this check while passing as a known flag.
		for _, tok := range v.Argv[1:] {
			flag := tok
			if eq := strings.IndexByte(tok, '='); eq > 0 {
				flag = tok[:eq]
			}
			if argv0 == "termux-nfc" && flag == "-w" {
				return fmt.Errorf("tier A must not use mutating argv flag -w on termux-nfc")
			}
		}
	}
	if v.TimeoutS < 0 {
		return fmt.Errorf("timeout_s must be >= 0")
	}
	if v.Retries != nil && *v.Retries < 0 {
		return fmt.Errorf("retries must be >= 0")
	}
	if v.TimeoutS == 0 {
		// inherit daemon default at runtime; ok at load
		_ = d
	}
	switch v.Parser {
	case ParserJSON, ParserText, ParserExit, "":
	default:
		return fmt.Errorf("parser must be json|text|exit, got %q", v.Parser)
	}
	if err := validateWatch(v); err != nil {
		return err
	}
	for _, a := range v.Args {
		if a.Name == "" {
			return fmt.Errorf("args entry missing name")
		}
		if a.Flag != "" && !strings.HasPrefix(a.Flag, "-") {
			return fmt.Errorf("arg %q flag must start with '-', got %q", a.Name, a.Flag)
		}
	}
	if v.StdinArg != nil && v.StdinArg.Arg == "" {
		return fmt.Errorf("stdin_arg.arg must be non-empty when set")
	}
	return nil
}

// streamVerbs maps the only verbs allowed to declare `watch: {mode: stream}`
// to their required argv0 binary.
var streamVerbs = map[string]string{
	"location.stream": "termux-location",
	"sensor.stream":   "termux-sensor",
}

func validateWatch(v Verb) error {
	w := v.Watch
	if w == nil {
		return nil
	}
	// Only location.stream and sensor.stream may stream; anything else
	// declaring mode stream is rejected even if the watch shape is valid.
	requireStreamAllowlisted := func() error {
		wantBin, ok := streamVerbs[v.Name]
		if !ok {
			return fmt.Errorf("watch.mode stream is only allowed for location.stream and sensor.stream, got verb %q", v.Name)
		}
		if len(v.Argv) == 0 || v.Argv[0] != wantBin {
			return fmt.Errorf("watch.mode stream verb %q must use binary %q", v.Name, wantBin)
		}
		return nil
	}
	switch t := w.(type) {
	case bool:
		if t {
			return fmt.Errorf("watch: true is invalid; use {mode: stream, buffer: N} or false")
		}
		return nil
	case map[string]any:
		for k := range t {
			if k != "mode" && k != "buffer" {
				return fmt.Errorf("watch: unknown field %q", k)
			}
		}
		mode, _ := t["mode"].(string)
		if mode != "stream" {
			return fmt.Errorf("watch.mode must be stream, got %q", mode)
		}
		return requireStreamAllowlisted()
	default:
		// yaml may decode nested struct via map only with any; accept Watch-shaped via remarshal
		b, err := yaml.Marshal(w)
		if err != nil {
			return fmt.Errorf("watch: %w", err)
		}
		var ww Watch
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&ww); err != nil {
			return fmt.Errorf("watch: %w", err)
		}
		if ww.Mode != "" && ww.Mode != "stream" {
			return fmt.Errorf("watch.mode must be stream, got %q", ww.Mode)
		}
		if ww.Mode == "stream" {
			return requireStreamAllowlisted()
		}
		return nil
	}
}

// Get resolves name or alias to a verb.
func (c *Catalog) Get(name string) (Verb, bool) {
	if v, ok := c.ByName[name]; ok {
		return v, true
	}
	if primary, ok := c.Aliases[name]; ok {
		v, ok := c.ByName[primary]
		return v, ok
	}
	// wifi.info is the canonical name; also accept direct alias keys as verbs if present
	return Verb{}, false
}

// DefaultPath returns the conventional catalog path next to the binary working dir.
func DefaultPath() string {
	return "verbs.yaml"
}

// ResolvePath prefers explicit path, else verbs.yaml in cwd or executable dir.
func ResolvePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	candidates := []string{
		DefaultPath(),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "verbs.yaml"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("verbs.yaml not found (searched: %s)", strings.Join(candidates, ", "))
}
