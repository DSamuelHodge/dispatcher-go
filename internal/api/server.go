// Package api is the loopback HTTP surface for dispatcher-go.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
	"github.com/DSamuelHodge/dispatcher-go/internal/execx"
	"github.com/DSamuelHodge/dispatcher-go/internal/notify"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/retry"
	"github.com/DSamuelHodge/dispatcher-go/internal/streams"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Server serves the /v1 API.
type Server struct {
	Catalog *verbs.Catalog
	Token   *auth.Token
	Tasks   queue.Store
	// Version is the daemon build version, surfaced in /v1/health.
	// Set from main.Version; empty means a dev build.
	Version   string
	Audit     *audit.Logger
	Circuits  *circuit.Registry
	Streams   *streams.Registry
	Resume    queue.ResumeStats
	StartedAt time.Time
	// Notifier delivers exhaustion alerts (defaults to Termux).
	Notifier notify.Notifier
	// SyncExec runs first exec inline (tests / simple mode).
	// When false, the task is left pending for the worker.
	SyncExec bool
}

// New constructs a server with Termux notifier defaults.
func New(cat *verbs.Catalog, tok *auth.Token, tasks queue.Store) *Server {
	return &Server{
		Catalog:   cat,
		Token:     tok,
		Tasks:     tasks,
		Notifier:  notify.Termux{},
		StartedAt: time.Now().UTC(),
		SyncExec:  true,
	}
}

// Handler returns the root HTTP handler with auth middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	// Progressive verb disclosure: search and get-one before list.
	mux.HandleFunc("GET /v1/verbs/search", s.handleSearchVerbs)
	mux.HandleFunc("GET /v1/verbs/{name}", s.handleGetVerb)
	mux.HandleFunc("GET /v1/verbs", s.handleListVerbs)
	mux.HandleFunc("POST /v1/verbs/{name}", s.handlePostVerb)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("POST /v1/streams", s.handlePostStream)
	mux.HandleFunc("GET /v1/streams/{id}", s.handleGetStream)
	mux.HandleFunc("DELETE /v1/streams/{id}", s.handleDeleteStream)
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cb := map[string]any{}
	if s.Circuits != nil {
		for k, v := range s.Circuits.Snapshots() {
			cb[k] = v
		}
	}
	ver := s.Version
	if ver == "" {
		ver = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"autonomy":    "full",
		"queue_depth": s.Tasks.Depth(),
		"cb_states":   cb,
		"resume":      s.Resume,
		"uptime_s":    int(time.Since(s.StartedAt).Seconds()),
		"version":     ver,
	})
}

func (s *Server) handleListVerbs(w http.ResponseWriter, r *http.Request) {
	detail := r.URL.Query().Get("detail")
	if detail == "" {
		detail = DetailSummary
	}
	out, err := listVerbsDetail(s.Catalog, detail)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_detail", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSearchVerbs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("query")
	}
	limit := 8
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("bad limit %q", raw), "")
			return
		}
		limit = n
	}
	writeJSON(w, http.StatusOK, searchVerbs(s.Catalog, q, limit))
}

func (s *Server) handleGetVerb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, ok := s.Catalog.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_verb", fmt.Sprintf("verb %q not found", name), "")
		return
	}
	writeJSON(w, http.StatusOK, verbFullEntry(v))
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
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
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

	// ADR-0003: idempotency key binds (verb, args, stdin) to one task.
	reqHash := idempotencyHash(v.Name, body.Args, body.Stdin)
	if body.IdempotencyKey != "" {
		if prior, found, err := s.Tasks.FindIdempotency(body.IdempotencyKey); err != nil {
			writeErr(w, http.StatusInternalServerError, "store_error", err.Error(), "")
			return
		} else if found {
			if prior.Verb != v.Name || prior.RequestHash != reqHash {
				writeErr(w, http.StatusConflict, "idempotency_conflict",
					fmt.Sprintf("idempotency key already used for a different request (task %s)", prior.TaskID), "")
				return
			}
			if t, ok := s.Tasks.Get(prior.TaskID); ok {
				writeJSON(w, http.StatusOK, map[string]any{
					"task_id": t.ID,
					"status":  t.State,
					"replay":  true,
				})
				return
			}
			// Task row gone: fall through and re-bind below.
		}
	}

	redactedArgs := approve.RedactArgs(v, body.Args)
	argsJSON, _ := json.Marshal(redactedArgs)
	maxRetries := s.Catalog.Daemon.MaxRetries
	if v.Retries != nil {
		maxRetries = *v.Retries
	}
	br := s.breakerFor(v)
	if br != nil {
		// Peek: if open and not yet half-open window, reject fast.
		// Allow() would consume half-open slot — use Snapshot.
		if sn := br.Snapshot(); sn.State == circuit.Open {
			writeErr(w, http.StatusServiceUnavailable, "circuit_open", fmt.Sprintf("circuit open for %s", v.Name), "")
			return
		}
	}
	// Capacity check + insert + audit happen atomically inside the store; a
	// loser under concurrency gets ErrQueueFull and we map it to 503.
	task, err := s.Tasks.CreateAndAuditLimited(queue.CreateInput{
		Verb:         v.Name,
		ArgsJSON:     string(argsJSON),
		Argv:         argv,
		ArgvRedacted: argvRedacted,
		Stdin:        body.Stdin,
		MaxRetries:   maxRetries,
	}, audit.Event{
		Verb: v.Name, Tier: string(v.Tier),
		State: queue.StateAccepted, ArgvRedacted: argvRedacted,
	}, s.Catalog.Daemon.MaxQueueDepth)
	if err != nil {
		if errors.Is(err, queue.ErrQueueFull) {
			writeErr(w, http.StatusServiceUnavailable, "queue_full", "task queue is full", "")
			return
		}
		writeErr(w, http.StatusInternalServerError, "store_error", err.Error(), "")
		return
	}

	if body.IdempotencyKey != "" {
		rec := queue.IdempotencyRecord{
			Key: body.IdempotencyKey, Verb: v.Name, RequestHash: reqHash, TaskID: task.ID,
		}
		if err := s.Tasks.SaveIdempotency(rec); err != nil {
			// Lost a same-key race: replay the winner, cancel our orphan
			// (still accepted — approval hasn't run yet, so nothing executed).
			if win, found, _ := s.Tasks.FindIdempotency(body.IdempotencyKey); found {
				_ = s.Tasks.UpdateAndAudit(task.ID, func(t *queue.Task) {
					t.State = queue.StateCanceled
					t.Error = "superseded by idempotent replay"
				}, audit.Event{
					TaskID: task.ID, Verb: v.Name, Tier: string(v.Tier),
					State: queue.StateCanceled, ArgvRedacted: argvRedacted, Error: "superseded by idempotent replay",
				})
				if t, ok := s.Tasks.Get(win.TaskID); ok {
					writeJSON(w, http.StatusOK, map[string]any{
						"task_id": t.ID,
						"status":  t.State,
						"replay":  true,
					})
					return
				}
			}
			// Non-duplicate save error, or winner unreadable: continue
			// with our own task (at-least-once beats dropping the request).
		}
	}

	// Execution is SyncExec (inline once) or pending for the worker.
	s.runPipeline(r.Context(), task.ID, v, br, argv, argvRedacted, body.Stdin)
	taskID := task.ID
	task, ok = s.Tasks.Get(taskID)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "store_error", fmt.Sprintf("task %q lost after create", taskID), "")
		return
	}
	// Flush the outbox rows appended by the pipeline above.
	_, _ = s.Tasks.DrainOutbox(s.Audit, 20)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id": task.ID,
		"status":  task.State,
	})
}

// breakerFor returns the verb's circuit breaker, or nil when unconfigured.
func (s *Server) breakerFor(v verbs.Verb) *circuit.Breaker {
	if s.Circuits == nil {
		return nil
	}
	trip := 0
	if v.CircuitBreakerThreshold != nil {
		trip = *v.CircuitBreakerThreshold
	}
	return s.Circuits.For(v.Name, trip)
}

// idempotencyHash binds verb + canonical args + stdin (hash only, secrets
// never stored) to one request identity.
func idempotencyHash(verb string, args map[string]any, stdin string) string {
	canonical, _ := json.Marshal(args)
	h := sha256.New()
	h.Write([]byte(verb))
	h.Write([]byte{0})
	h.Write(canonical)
	h.Write([]byte{0})
	h.Write([]byte(stdin))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) runPipeline(ctx context.Context, id string, v verbs.Verb, br *circuit.Breaker, argv, argvRedacted []string, stdin string) {
	// Full autonomy: no human gate. Audit accept→execute path only.
	_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
		// no-op marker; state advances in executeOnce or pending handoff
	}, audit.Event{
		TaskID: id, Verb: v.Name, Tier: string(v.Tier),
		State: "accepted", ArgvRedacted: argvRedacted,
	})

	runStdin := ""
	if v.StdinArg != nil {
		runStdin = stdin
	}

	if !s.SyncExec {
		_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
			t.State = queue.StatePending
		}, audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier),
			State: queue.StatePending, ArgvRedacted: argvRedacted,
		})
		return
	}
	maxRetries := s.Catalog.Daemon.MaxRetries
	if v.Retries != nil {
		maxRetries = *v.Retries
	}
	s.executeOnce(ctx, id, v, br, maxRetries, argv, argvRedacted, runStdin)
}

func (s *Server) executeOnce(ctx context.Context, id string, v verbs.Verb, br *circuit.Breaker, maxRetries int, argv, argvRedacted []string, stdin string) {
	_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
		t.State = queue.StateExecuting
		t.Attempt = 0
	}, audit.Event{
		TaskID: id, Verb: v.Name, Tier: string(v.Tier),
		State: queue.StateExecuting, ArgvRedacted: argvRedacted, Attempt: 0,
	})
	start := time.Now()
	timeout := time.Duration(s.Catalog.Daemon.TaskTimeoutS) * time.Second
	if v.TimeoutS > 0 {
		timeout = time.Duration(v.TimeoutS) * time.Second
	}
	res := execx.Run(ctx, argv, stdin, timeout)
	end := time.Now()
	lat := end.Sub(start).Milliseconds()
	stdout := scrubOutput(res.Stdout, stdin)
	stderr := scrubOutput(res.Stderr, stdin)

	outcome := "ok"
	errMsg := ""
	ec := res.ExitCode
	if res.TimedOut {
		outcome = "timeout"
		if res.Err != nil {
			errMsg = res.Err.Error()
		} else {
			errMsg = "timeout"
		}
	} else if res.Err != nil || res.ExitCode != 0 {
		outcome = "failed"
		if res.Err != nil {
			errMsg = res.Err.Error()
		} else {
			errMsg = fmt.Sprintf("exit %d", res.ExitCode)
		}
	}
	_ = s.Tasks.RecordAttempt(id, 0, start, end, &ec, outcome, errMsg)

	if outcome == "ok" {
		if br != nil {
			br.Success()
		}
		var result any
		if v.Parser == verbs.ParserJSON || v.Parser == "" {
			if parsed, err := execx.ParseJSON(res.Stdout); err == nil {
				result = parsed
			}
		}
		_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
			t.State = queue.StateExecuted
			t.LastAttemptOutcome = "ok"
			t.ExitCode = &ec
			t.Stdout = stdout
			t.Stderr = stderr
			t.Result = result
			t.Error = ""
			t.NextRunAt = nil
		}, audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier),
			State: queue.StateExecuted, ArgvRedacted: argvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: 0,
		})
		return
	}

	// failed / timeout: record the verdict, then retry or exhaust (spec §9).
	if br != nil {
		br.Failure()
	}
	if retry.ShouldExhaust(0, maxRetries) {
		_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
			t.State = queue.StateExhausted
			t.LastAttemptOutcome = outcome
			t.ExitCode = &ec
			t.Stdout = stdout
			t.Stderr = stderr
			t.Error = errMsg
			t.NextRunAt = nil
		}, audit.Event{
			TaskID: id, Verb: v.Name, Tier: string(v.Tier),
			State: queue.StateExhausted, ArgvRedacted: argvRedacted, ExitCode: &ec,
			LatencyMS: lat, Attempt: 0, Error: errMsg,
		})
		if s.Notifier == nil {
			s.Notifier = notify.Termux{}
		}
		_ = s.Notifier.Exhausted(ctx, v.Name, id, 1)
		return
	}
	delay := retry.DelayAfterFailure(s.backoffBase(), 0, maxServerJitter)
	next := time.Now().UTC().Add(delay)
	_ = s.Tasks.UpdateAndAudit(id, func(t *queue.Task) {
		t.State = queue.StateRetryScheduled
		t.LastAttemptOutcome = outcome
		t.ExitCode = &ec
		t.Stdout = stdout
		t.Stderr = stderr
		t.Error = errMsg
		t.Attempt = 1
		t.NextRunAt = &next
	}, audit.Event{
		TaskID: id, Verb: v.Name, Tier: string(v.Tier),
		State: "will-retry", ArgvRedacted: argvRedacted, ExitCode: &ec,
		LatencyMS: lat, Attempt: 0, Error: errMsg,
	})
}

// maxServerJitter mirrors the worker default (spec: jitter <= 250ms).
const maxServerJitter = 250 * time.Millisecond

func (s *Server) backoffBase() time.Duration {
	return time.Duration(s.Catalog.Daemon.BackoffBaseS * float64(time.Second))
}

// scrubOutput withholds child output that echoes a stdin secret: stdin
// bodies are never persisted or served, even second-hand via stdout.
func scrubOutput(out, stdinSecret string) string {
	if stdinSecret != "" && strings.Contains(out, stdinSecret) {
		return approve.RedactedMarker
	}
	return out
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
	writeJSON(w, http.StatusOK, projectTask(t, r.URL.Query()))
}

// projectTask applies progressive result disclosure for agents.
// Default is compact (no large stdout/stderr). Use detail=full for the raw task row.
// fields= comma-list selects keys; max_stdout / max_stderr truncate byte lengths.
func projectTask(t *queue.Task, q url.Values) any {
	if t == nil {
		return nil
	}
	detail := strings.ToLower(strings.TrimSpace(q.Get("detail")))
	if detail == DetailFull || detail == "all" || detail == "raw" {
		return t
	}
	maxOut := 512
	if raw := q.Get("max_stdout"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			maxOut = n
		}
	}
	maxErr := maxOut
	if raw := q.Get("max_stderr"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			maxErr = n
		}
	}
	compact := map[string]any{
		"id":      t.ID,
		"verb":    t.Verb,
		"state":   t.State,
		"attempt": t.Attempt,
	}
	if t.ExitCode != nil {
		compact["exit_code"] = *t.ExitCode
	}
	if t.Error != "" {
		compact["error"] = t.Error
	}
	if t.Result != nil {
		compact["result"] = t.Result
	}
	if t.LastAttemptOutcome != "" {
		compact["last_attempt_outcome"] = t.LastAttemptOutcome
	}
	// Include truncated stdout/stderr only when explicitly requested via fields
	// or when detail=stdout.
	fieldsRaw := strings.TrimSpace(q.Get("fields"))
	wantStdout := detail == "stdout" || detail == "output"
	wantStderr := wantStdout
	wantArgv := false
	if fieldsRaw != "" {
		// Start from empty selection when fields is set.
		selected := map[string]any{"id": t.ID}
		for _, f := range strings.Split(fieldsRaw, ",") {
			f = strings.TrimSpace(strings.ToLower(f))
			switch f {
			case "id":
				selected["id"] = t.ID
			case "verb":
				selected["verb"] = t.Verb
			case "state", "status":
				selected["state"] = t.State
			case "attempt":
				selected["attempt"] = t.Attempt
			case "exit_code":
				if t.ExitCode != nil {
					selected["exit_code"] = *t.ExitCode
				}
			case "error":
				if t.Error != "" {
					selected["error"] = t.Error
				}
			case "result":
				if t.Result != nil {
					selected["result"] = t.Result
				}
			case "stdout":
				wantStdout = true
			case "stderr":
				wantStderr = true
			case "argv_redacted", "argv":
				wantArgv = true
			case "created_at":
				selected["created_at"] = t.CreatedAt
			case "updated_at":
				selected["updated_at"] = t.UpdatedAt
			case "last_attempt_outcome":
				if t.LastAttemptOutcome != "" {
					selected["last_attempt_outcome"] = t.LastAttemptOutcome
				}
			}
		}
		if wantStdout && t.Stdout != "" {
			selected["stdout"] = truncateStr(t.Stdout, maxOut)
		}
		if wantStderr && t.Stderr != "" {
			selected["stderr"] = truncateStr(t.Stderr, maxErr)
		}
		if wantArgv {
			selected["argv_redacted"] = t.ArgvRedacted
		}
		return selected
	}
	if wantStdout && t.Stdout != "" {
		compact["stdout"] = truncateStr(t.Stdout, maxOut)
	}
	if wantStderr && t.Stderr != "" {
		compact["stderr"] = truncateStr(t.Stderr, maxErr)
	}
	return compact
}

func truncateStr(s string, max int) string {
	if max < 0 || len(s) <= max {
		return s
	}
	if max == 0 {
		return ""
	}
	// Prefer rune-safe cut for display; byte cut is fine for caps.
	if max < 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.Tasks.List(state)})
}

func (s *Server) handlePostStream(w http.ResponseWriter, r *http.Request) {
	if s.Streams == nil {
		writeErr(w, http.StatusServiceUnavailable, "streams_disabled", "stream registry not configured", "")
		return
	}
	var body struct {
		Verb string         `json:"verb"`
		Args map[string]any `json:"args"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid_json", err.Error(), "")
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	v, ok := s.Catalog.Get(body.Verb)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_verb", fmt.Sprintf("verb %q not found", body.Verb), "")
		return
	}
	isWatch := false
	switch wch := v.Watch.(type) {
	case map[string]any:
		if m, _ := wch["mode"].(string); m == "stream" {
			isWatch = true
		}
	}
	if !isWatch {
		writeErr(w, http.StatusBadRequest, "not_a_stream", fmt.Sprintf("%q is not a watch stream verb", body.Verb), "")
		return
	}
	argv, argvRedacted, err := expandArgv(v, body.Args, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_error", err.Error(), "")
		return
	}
	// Streams start immediately under full agent autonomy.
	br := s.breakerFor(v)
	if br != nil {
		if sn := br.Snapshot(); sn.State == circuit.Open {
			writeErr(w, http.StatusServiceUnavailable, "circuit_open", fmt.Sprintf("circuit open for %s", v.Name), "")
			return
		}
	}
	st, err := s.Streams.Start(v, argv)
	if err != nil {
		if br != nil {
			br.Failure()
		}
		writeErr(w, http.StatusInternalServerError, "stream_start_failed", err.Error(), "")
		return
	}
	if br != nil {
		br.Success()
	}
	_ = s.audit(audit.Event{
		TaskID: st.ID, Verb: v.Name, Tier: string(v.Tier),
		State: "stream_open", ArgvRedacted: argvRedacted,
	})
	_, _ = s.Tasks.DrainOutbox(s.Audit, 20)
	writeJSON(w, http.StatusAccepted, map[string]any{"stream_id": st.ID, "verb": st.Verb})
}

func (s *Server) handleGetStream(w http.ResponseWriter, r *http.Request) {
	if s.Streams == nil {
		writeErr(w, http.StatusNotFound, "unknown_stream", "no streams", "")
		return
	}
	id := r.PathValue("id")
	st, ok := s.Streams.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_stream", fmt.Sprintf("stream %q not found", id), id)
		return
	}
	var since uint64
	if q := r.URL.Query().Get("since"); q != "" {
		if _, err := fmt.Sscanf(q, "%d", &since); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_since", fmt.Sprintf("bad ?since=%q", q), id)
			return
		}
	}
	events := st.Ring.Since(since)
	writeJSON(w, http.StatusOK, map[string]any{
		"stream_id": st.ID,
		"verb":      st.Verb,
		"events":    events,
		"buffered":  st.Ring.Len(),
	})
}

func (s *Server) handleDeleteStream(w http.ResponseWriter, r *http.Request) {
	if s.Streams == nil {
		writeErr(w, http.StatusNotFound, "unknown_stream", "no streams", "")
		return
	}
	id := r.PathValue("id")
	st, ok := s.Streams.Get(id)
	verb := ""
	if ok {
		verb = st.Verb
	}
	if err := s.Streams.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, "unknown_stream", err.Error(), id)
		return
	}
	_ = s.audit(audit.Event{TaskID: id, Verb: verb, State: "stream_closed"})
	writeJSON(w, http.StatusOK, map[string]any{"stream_id": id, "status": "closed"})
}

// solePlaceholder reports whether tok is exactly one {{.name}} template
// with no surrounding literal text.
func solePlaceholder(tok string) (string, bool) {
	if !strings.HasPrefix(tok, "{{.") || !strings.HasSuffix(tok, "}}") {
		return "", false
	}
	name := tok[len("{{.") : len(tok)-len("}}")]
	if name == "" || strings.ContainsAny(name, "./{} ") {
		return "", false
	}
	return name, true
}

func expandArgv(v verbs.Verb, args map[string]any, stdin string) (argv, redacted []string, err error) {
	specs := make(map[string]verbs.ArgSpec, len(v.Args))
	for _, a := range v.Args {
		specs[a.Name] = a
		if a.Required {
			if _, ok := args[a.Name]; !ok {
				return nil, nil, fmt.Errorf("missing required arg %q", a.Name)
			}
		}
	}
	type expanded struct {
		raw    string
		out    string
		isTmpl bool
		fields []string
	}
	ex := make([]expanded, len(v.Argv))
	for i, tok := range v.Argv {
		out, isTmpl, fields := subst(tok, args)
		ex[i] = expanded{raw: tok, out: out, isTmpl: isTmpl, fields: fields}
	}
	// Omit empty non-required flag+value pairs: a token that is exactly
	// {{.name}} rendering "" drops itself, plus its declared flag token
	// when the preceding token is that flag verbatim.
	drop := make([]bool, len(ex))
	for i, e := range ex {
		name, ok := solePlaceholder(e.raw)
		if !ok || e.out != "" {
			continue
		}
		spec, known := specs[name]
		if !known || spec.Required {
			continue
		}
		drop[i] = true
		if i > 0 && !drop[i-1] && spec.Flag != "" && !ex[i-1].isTmpl && ex[i-1].raw == spec.Flag {
			drop[i-1] = true
		}
	}
	secrets := approve.SecretFields(v)
	for i, e := range ex {
		if drop[i] {
			continue
		}
		out := e.out
		red := out
		if e.isTmpl {
			// Every placeholder in the token must be checked, not just
			// the last: "{{.a}}-{{.b}}" with either secret redacts.
			for _, name := range e.fields {
				if _, sec := secrets[name]; sec {
					red = approve.RedactedMarker
					break
				}
			}
		}
		if stdin != "" && strings.Contains(out, stdin) {
			red = approve.RedactedMarker
		}
		argv = append(argv, out)
		redacted = append(redacted, red)
	}
	return argv, redacted, nil
}

func subst(tok string, args map[string]any) (out string, isTemplate bool, fields []string) {
	const start, end = "{{.", "}}"
	if !strings.Contains(tok, start) {
		return tok, false, nil
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
		fields = append(fields, name)
		val, ok := args[name]
		if !ok {
			val = ""
		}
		out = out[:i] + fmt.Sprint(val) + out[j+len(end):]
	}
	return out, isTemplate, fields
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
