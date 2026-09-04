// Package streams manages watch subscriptions with bounded ring buffers.
package streams

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// MaxBufferSize caps the per-stream ring buffer (watch.buffer).
const MaxBufferSize = 4096

// MaxStreams caps concurrent active streams per registry.
const MaxStreams = 16

// ErrTooManyStreams is returned by Start when active streams >= MaxStreams.
var ErrTooManyStreams = errors.New("too many streams")

// ErrInvalidBuffer is returned by Start when the resolved buffer is <= 0 or > MaxBufferSize.
var ErrInvalidBuffer = errors.New("invalid stream buffer size")

// Stream is one live subscription.
type Stream struct {
	ID        string
	Verb      string
	Argv      []string
	CreatedAt time.Time
	Ring      *Ring
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
	mu        sync.Mutex
}

// Registry tracks active streams.
type Registry struct {
	mu         sync.Mutex
	streams    map[string]*Stream
	defaultBuf int
}

// NewRegistry creates an empty registry.
func NewRegistry(defaultBuf int) *Registry {
	if defaultBuf <= 0 {
		defaultBuf = 128
	}
	return &Registry{streams: make(map[string]*Stream), defaultBuf: defaultBuf}
}

// Start launches argv as a process group and tees stdout lines into a ring.
func (r *Registry) Start(v verbs.Verb, argv []string) (*Stream, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	buf, err := resolveBuffer(v.Watch, r.defaultBuf)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if len(r.streams) >= MaxStreams {
		r.mu.Unlock()
		return nil, ErrTooManyStreams
	}
	r.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // merge
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	id := newID()
	s := &Stream{
		ID:        id,
		Verb:      v.Name,
		Argv:      append([]string(nil), argv...),
		CreatedAt: time.Now().UTC(),
		Ring:      NewRing(buf),
		cmd:       cmd,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	go s.readLoop(stdout)
	r.mu.Lock()
	if len(r.streams) >= MaxStreams {
		r.mu.Unlock()
		// Raced past the cap while the process was starting: tear it down
		// without registering so the count stays bounded.
		s.Kill()
		return nil, ErrTooManyStreams
	}
	r.streams[id] = s
	r.mu.Unlock()
	return s, nil
}

// resolveBuffer maps watch.buffer (YAML) plus the registry default into a
// validated ring capacity in [1, MaxBufferSize].
func resolveBuffer(watch any, def int) (int, error) {
	if def <= 0 || def > MaxBufferSize {
		return 0, fmt.Errorf("%w: %d (want 1..%d)", ErrInvalidBuffer, def, MaxBufferSize)
	}
	buf := def
	switch w := watch.(type) {
	case nil:
		// no override
	case map[string]any:
		raw, ok := w["buffer"]
		if !ok {
			break
		}
		n, ok := asInt(raw)
		if !ok {
			// Non-numeric buffer: ignore and keep default (preserves old
			// behavior for unexpected YAML shapes; validation of the
			// catalog schema itself lives in internal/verbs).
			break
		}
		if n <= 0 || n > MaxBufferSize {
			return 0, fmt.Errorf("%w: %d (want 1..%d)", ErrInvalidBuffer, n, MaxBufferSize)
		}
		buf = n
	case verbs.Watch:
		if w.Buffer == 0 {
			break // unset
		}
		if w.Buffer <= 0 || w.Buffer > MaxBufferSize {
			return 0, fmt.Errorf("%w: %d (want 1..%d)", ErrInvalidBuffer, w.Buffer, MaxBufferSize)
		}
		buf = w.Buffer
	case *verbs.Watch:
		if w == nil || w.Buffer == 0 {
			break
		}
		if w.Buffer <= 0 || w.Buffer > MaxBufferSize {
			return 0, fmt.Errorf("%w: %d (want 1..%d)", ErrInvalidBuffer, w.Buffer, MaxBufferSize)
		}
		buf = w.Buffer
	default:
		// Unknown watch shape: keep default.
	}
	if buf <= 0 || buf > MaxBufferSize {
		return 0, fmt.Errorf("%w: %d (want 1..%d)", ErrInvalidBuffer, buf, MaxBufferSize)
	}
	return buf, nil
}

// asInt coerces YAML/JSON numeric shapes into an int.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func (s *Stream) readLoop(rc io.ReadCloser) {
	defer close(s.done)
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	// larger lines for sensor dumps
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		s.Ring.Push(time.Now().UTC().Format(time.RFC3339Nano), sc.Text())
	}
	if err := sc.Err(); err != nil {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}
	_ = s.cmd.Wait()
}

// Err returns the scanner/read-loop failure for a stream, if any.
// A non-nil error (e.g. bufio "token too long" past the 1MB scanner limit)
// means the stream is dead: no further events will arrive.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Get returns a stream by id.
func (r *Registry) Get(id string) (*Stream, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.streams[id]
	return s, ok
}

// Delete kills the process group and removes the stream.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	s, ok := r.streams[id]
	if ok {
		delete(r.streams, id)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("stream %q not found", id)
	}
	s.Kill()
	return nil
}

// Kill terminates the child process group.
func (s *Stream) Kill() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// kill process group
		pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = s.cmd.Process.Kill()
		}
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

// Count returns active streams.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

// CloseAll stops every stream (daemon shutdown) concurrently.
// Each Kill can block up to 2s; with N capped at MaxStreams (16) a serial
// close would worst-case take ~32s and blow past shutdown grace, so fan out
// and wait. Delete is safe for concurrent use.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.streams))
	for id := range r.streams {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = r.Delete(id)
		}(id)
	}
	wg.Wait()
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
