// Package streams manages watch subscriptions with bounded ring buffers.
package streams

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

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
	buf := r.defaultBuf
	// watch buffer from verb if present
	if m, ok := v.Watch.(map[string]any); ok {
		if b, ok := m["buffer"].(int); ok && b > 0 {
			buf = b
		}
		// yaml may decode numbers as int or float
		if b, ok := m["buffer"].(float64); ok && b > 0 {
			buf = int(b)
		}
	}
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
	r.streams[id] = s
	r.mu.Unlock()
	return s, nil
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

// CloseAll stops every stream (daemon shutdown).
func (r *Registry) CloseAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.streams))
	for id := range r.streams {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.Delete(id)
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
