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

	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Server serves the /v1 API.
type Server struct {
	Catalog   *verbs.Catalog
	Token     *auth.Token
	Tasks     queue.Store
	Policy    approve.PolicyFile
	Prompter  approve.Prompter
	Audit     *audit.Logger
	StartedAt time.Time
	// SyncExec runs approval+first-exec inline (tests / simple mode).
	// When false, after approval the task is left pending for the worker.
	SyncExec bool
}

// New constructs a server with dialog prompter by default.
func New(cat *verbs.Catalog, tok *auth.Token, tasks queue.Store) *Server {
	return &Server{
		Catalog:   cat,
		Token:     tok,
		Tasks:     tasks,
		Prompter:  approve.DialogPrompter{},
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
		WriteTimeout:      180 * time.Second,
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

func (s *Server) effectiveMode() string {
	if s.Policy.ApprovalMode != "" {
		return s.Policy.ApprovalMode
	}
	return string(s.Catalog.Daemon.ApprovalMode)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":        s.effectiveMode(),
		"queue_depth": s.Tasks.Depth(),
		"cb_states":   map[string]any{},
		"uptime_s":    int(time.Since(s.StartedAt).Seconds()),
		"version":     "m4",
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
	if s.Tasks.Depth() >= s.Catalog.Daemon.MaxQueueDepth {
		writeErr(w, http.StatusServiceUnavailable, "queue_full", "task queue is full", "")
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
	if v.StdinArg != nil {
		delete(body.Args, v.StdinArg.Arg)
	}

	argv, argvRedacted, err := expandArgv(v, body.Args, body.Stdin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error(), "")
		return
	}

	redactedArgs := approve.RedactArgs(v, body.Args)
	argsJSON, _ := json.Marshal(redactedArgs)
	maxRetries := s.Catalog.Daemon.MaxRetries
	if v.Retries != nil {
		maxRetries = *v.Retries
	}
	task, err := s.Tasks.Create(queue.CreateInput{
		Verb:         v.Name,
		ArgsJSON:     string(argsJSON),
		Argv:         argv,
		ArgvRedacted: argvRedacted,
		Stdin:        body.Stdin,
		MaxRetries:   maxRetries,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error(), "")
		return
	}

	_ = s.audit(audit.Event{
		TaskID: task.ID, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
		State: queue.StateAccepted, ArgvRedacted: argvRedacted, Approval: string(v.Approval),
	})

	// Approval runs inline; execution is SyncExec (inline once) or pending for worker.
	s.runPipeline(r.Context(), task.ID, v, argv, argvRedacted, body.Stdin)
	task, _ = s.Tasks.Get(task.ID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": task.ID,
		"status":  task.State,
	})
}

func (s *Server) runPipeline(ctx context.Context, id string, v verbs.Verb, argv, argvRedacted []string, stdin string) {
	dec := approve.Resolve(v, s.Catalog.Daemon, s.Policy)
	_ = s.Tasks.Update(id, func(t *queue.Task) {
		t.ApprovalMode = string(dec.Mode)
	})

	if dec.NeedsPrompt {
		_ = s.Tasks.Update(id, func(t *queue.Task) {
			t.State = queue.StatePendingApproval
		})
		_ = s.audit(audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
			State: queue.StatePendingApproval, ArgvRedacted: argvRedacted, Approval: string(dec.Mode),
		})

		prompter := s.Prompter
		if prompter == nil {
			prompter = approve.DialogPrompter{}
		}
		title := approve.DialogTitle(v.Name)
		body := approve.DialogBody(v, argvRedacted)
		if approve.ContainsSecret(body, stdin) {
			body = v.Name + " " + approve.RedactedMarker
		}
		res := prompter.Confirm(ctx, title, body, approve.DefaultPromptTimeout)
		if res.TimedOut || res.Err != nil || !res.Approved {
			errMsg := "denied"
			if res.TimedOut {
				errMsg = "approval timeout"
			} else if res.Err != nil {
				errMsg = res.Err.Error()
			}
			_ = s.Tasks.Update(id, func(t *queue.Task) {
				t.State = queue.StateDenied
				t.Error = errMsg
			})
			_ = s.audit(audit.Event{
				TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
				State: queue.StateDenied, ArgvRedacted: argvRedacted, Approval: string(dec.Mode),
				Error: errMsg,
			})
			return
		}
		_ = s.Tasks.Update(id, func(t *queue.Task) {
			t.ApprovedBy = "user"
		})
		_ = s.audit(audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
			State: "approved", ApprovedBy: "user", ArgvRedacted: argvRedacted, Approval: string(dec.Mode),
		})
	} else {
		_ = s.Tasks.Update(id, func(t *queue.Task) {
			t.ApprovedBy = dec.By
		})
		_ = s.audit(audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
			State: "approved", ApprovedBy: dec.By, ArgvRedacted: argvRedacted, Approval: string(dec.Mode),
		})
	}

	runStdin := ""
	if v.StdinArg != nil {
		runStdin = stdin
	}

	if !s.SyncExec {
		_ = s.Tasks.Update(id, func(t *queue.Task) {
			t.State = queue.StatePending
			// persist stdin already on create
		})
		return
	}
	// SyncExec: single attempt inline (retries handled by worker on fail path in M4+ via re-queue optional)
	s.executeOnce(ctx, id, v, argv, argvRedacted, runStdin)
}

func (s *Server) executeOnce(ctx context.Context, id string, v verbs.Verb, argv, argvRedacted []string, stdin string) {
	_ = s.Tasks.Update(id, func(t *queue.Task) {
		t.State = queue.StateExecuting
		t.Attempt = 0
	})
	_ = s.audit(audit.Event{
		TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
		State: queue.StateExecuting, ArgvRedacted: argvRedacted, Attempt: 0,
	})
	start := time.Now()
	timeout := time.Duration(s.Catalog.Daemon.TaskTimeoutS) * time.Second
	if v.TimeoutS > 0 {
		timeout = time.Duration(v.TimeoutS) * time.Second
	}
	res := execx.Run(ctx, argv, stdin, timeout)
	lat := time.Since(start).Milliseconds()

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
				b, _ := json.Marshal(parsed)
				t.ResultJSON = string(b)
			}
		}
		t.State = queue.StateExecuted
		t.LastAttemptOutcome = "ok"
	})
	task, _ := s.Tasks.Get(id)
	_ = s.audit(audit.Event{
		TaskID: id, Verb: v.Name, Tier: string(v.Tier), Risk: string(v.Risk),
		State: task.State, ArgvRedacted: argvRedacted, ExitCode: task.ExitCode,
		LatencyMS: lat, Attempt: 0, Error: task.Error,
	})
}

func (s *Server) audit(ev audit.Event) error {
	if s.Tasks == nil {
		if s.Audit != nil {
			return s.Audit.Log(ev)
		}
		return nil
	}
	if err := s.Tasks.AppendAudit(ev); err != nil {
		return err
	}
	_, err := s.Tasks.DrainOutbox(s.Audit, 20)
	return err
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

func expandArgv(v verbs.Verb, args map[string]any, stdin string) (argv, redacted []string, err error) {
	for _, a := range v.Args {
		if !a.Required {
			continue
		}
		if _, ok := args[a.Name]; !ok {
			return nil, nil, fmt.Errorf("missing required arg %q", a.Name)
		}
	}
	secrets := approve.SecretFields(v)
	argv = make([]string, len(v.Argv))
	redacted = make([]string, len(v.Argv))
	for i, tok := range v.Argv {
		out, isTmpl, name := subst(tok, args)
		argv[i] = out
		redacted[i] = out
		if isTmpl {
			if _, sec := secrets[name]; sec {
				redacted[i] = approve.RedactedMarker
			}
		}
		if stdin != "" && argv[i] == stdin {
			redacted[i] = approve.RedactedMarker
		}
	}
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
