// Package api is the loopback HTTP surface for dispatcher-go.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Server serves the /v1 API.
type Server struct {
	Catalog   *verbs.Catalog
	Token     *auth.Token
	Tasks     *queue.Memory
	StartedAt time.Time
	// SyncExec, when true, runs the verb inline before returning 202 (M2 default
	// for simple E2E; async worker arrives with M4).
	SyncExec bool
}

// New constructs a server.
func New(cat *verbs.Catalog, tok *auth.Token, tasks *queue.Memory) *Server {
	return &Server{
		Catalog:   cat,
		Token:     tok,
		Tasks:     tasks,
		StartedAt: time.Now().UTC(),
		SyncExec:  true,
	}
}

// Handler returns the root HTTP handler with auth middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/verbs", s.handleListVerbs)
	mux.HandleFunc("POST /v1/verbs/{name}", s.handlePostVerb)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	return s.withAuth(mux)
}

// ListenAndServe binds addr (must be loopback) and serves until ctx cancel.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen addr: %w", err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("refusing non-loopback bind %q", host)
	}
	// Resolve and double-check.
	ips, err := net.LookupIP(host)
	if err == nil {
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return fmt.Errorf("refusing non-loopback resolve of %q → %v", host, ip)
			}
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get(auth.Header)
		if !s.Token.Valid(provided) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing X-Agent-Token", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":        s.Catalog.Daemon.ApprovalMode,
		"queue_depth": s.Tasks.Depth(),
		"cb_states":   map[string]any{},
		"uptime_s":    int(time.Since(s.StartedAt).Seconds()),
		"version":     "m2",
	})
}

func (s *Server) handleListVerbs(w http.ResponseWriter, r *http.Request) {
	list := make([]map[string]any, 0, len(s.Catalog.Order))
	for _, name := range s.Catalog.Order {
		v := s.Catalog.ByName[name]
		list = append(list, map[string]any{
			"name":     v.Name,
			"tier":     v.Tier,
			"risk":     v.Risk,
			"approval": v.Approval,
			"argv":     v.Argv,
			"parser":   v.Parser,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"verbs": list})
}

type postVerbBody struct {
	Args           map[string]any `json:"args"`
	Stdin          string         `json:"stdin"`
	IdempotencyKey string         `json:"idempotency_key"`
}

func (s *Server) handlePostVerb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, ok := s.Catalog.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_verb", fmt.Sprintf("verb %q not found", name), "")
		return
	}
	var body postVerbBody
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			if err != io.EOF {
				writeErr(w, http.StatusBadRequest, "invalid_json", err.Error(), "")
				return
			}
		}
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}

	argv, argvRedacted, err := expandArgv(v, body.Args, body.Stdin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error(), "")
		return
	}
	argsJSON, _ := json.Marshal(body.Args)
	task := s.Tasks.Create(v.Name, argvRedacted, string(argsJSON))

	if s.SyncExec {
		s.execute(r.Context(), task.ID, v, argv, body.Stdin)
		task, _ = s.Tasks.Get(task.ID)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": task.ID,
		"status":  task.State,
	})
}

func (s *Server) execute(ctx context.Context, id string, v verbs.Verb, argv []string, stdin string) {
	_ = s.Tasks.Update(id, func(t *queue.Task) {
		t.State = queue.StateExecuting
		t.Attempt = 0
	})
	timeout := time.Duration(s.Catalog.Daemon.TaskTimeoutS) * time.Second
	if v.TimeoutS > 0 {
		timeout = time.Duration(v.TimeoutS) * time.Second
	}
	res := execx.Run(ctx, argv, stdin, timeout)

	_ = s.Tasks.Update(id, func(t *queue.Task) {
		ec := res.ExitCode
		t.ExitCode = &ec
		t.Stdout = res.Stdout
		t.Stderr = res.Stderr
		if res.TimedOut {
			t.State = queue.StateTimeout
			t.LastAttemptOutcome = "timeout"
			t.Error = res.Err.Error()
			return
		}
		if res.Err != nil || res.ExitCode != 0 {
			t.State = queue.StateFailed
			t.LastAttemptOutcome = "failed"
			if res.Err != nil {
				t.Error = res.Err.Error()
			} else {
				t.Error = fmt.Sprintf("exit %d", res.ExitCode)
			}
			return
		}
		if v.Parser == verbs.ParserJSON || v.Parser == "" {
			if parsed, err := execx.ParseJSON(res.Stdout); err == nil {
				t.Result = parsed
			}
		}
		t.State = queue.StateExecuted
		t.LastAttemptOutcome = "ok"
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.Tasks.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_task", fmt.Sprintf("task %q not found", id), id)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.Tasks.List(state)})
}

// expandArgv substitutes {{.name}} from args; stdin fields are redacted in argv_redacted.
func expandArgv(v verbs.Verb, args map[string]any, stdin string) (argv, redacted []string, err error) {
	// required args
	for _, a := range v.Args {
		if !a.Required {
			continue
		}
		if _, ok := args[a.Name]; !ok {
			return nil, nil, fmt.Errorf("missing required arg %q", a.Name)
		}
	}
	argv = make([]string, len(v.Argv))
	redacted = make([]string, len(v.Argv))
	for i, tok := range v.Argv {
		out, isTmpl, name := subst(tok, args)
		if isTmpl && name != "" {
			if _, ok := args[name]; !ok && !strings.Contains(tok, "{{") {
				// unreachable
			}
		}
		argv[i] = out
		redacted[i] = out
		if isTmpl {
			// if this template refers to stdin_arg, redact
			if v.StdinArg != nil && name == v.StdinArg.Arg {
				redacted[i] = "[REDACTED]"
			}
		}
	}
	_ = stdin
	// If stdin_arg set, ensure we don't put secret in argv (already separated).
	return argv, redacted, nil
}

func subst(tok string, args map[string]any) (out string, isTemplate bool, field string) {
	const start, end = "{{.", "}}"
	if !strings.Contains(tok, start) {
		return tok, false, ""
	}
	isTemplate = true
	out = tok
	for {
		i := strings.Index(out, start)
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], end)
		if j < 0 {
			break
		}
		j += i
		name := out[i+len(start) : j]
		field = name
		val, ok := args[name]
		if !ok {
			// leave empty string for optional missing
			val = ""
		}
		out = out[:i] + fmt.Sprint(val) + out[j+len(end):]
	}
	return out, isTemplate, field
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TaskID  string `json:"task_id,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, code, msg, taskID string) {
	writeJSON(w, status, errBody{Code: code, Message: msg, TaskID: taskID})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
