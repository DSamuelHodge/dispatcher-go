// Command dispatcher is the Termux:API loopback daemon entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DSamuelHodge/dispatcher-go/internal/api"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/circuit"
	"github.com/DSamuelHodge/dispatcher-go/internal/notify"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/streams"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
	"github.com/DSamuelHodge/dispatcher-go/internal/worker"
)

// Version is set via -ldflags "-X main.Version=..." at release build time.
var Version = "0.9.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("dispatcher", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	catalogPath := fs.String("catalog", "", "path to verbs.yaml (default: ./verbs.yaml)")
	validateOnly := fs.Bool("validate", false, "load and validate verbs.yaml then exit")
	tokenPath := fs.String("token-file", auth.DefaultFileName, "path to agent token file (0600)")
	dataDir := fs.String("data-dir", ".", "base directory for token/logs/db relative paths")
	auditPath := fs.String("audit-log", "", "NDJSON audit log path (default: <data-dir>/logs/audit.log)")
	dbPath := fs.String("db", "", "SQLite tasks db (default: <data-dir>/data/tasks.db)")
	syncExec := fs.Bool("sync-exec", false, "run first attempt inline after accept (debug); default uses worker")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Printf("dispatcher-go %s\n", Version)
		return 0
	}

	path, err := verbs.ResolvePath(*catalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: %v\n", err)
		return 1
	}
	cat, err := verbs.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: catalog: %v\n", err)
		return 1
	}
	if *validateOnly {
		fmt.Printf("dispatcher-go %s\n", Version)
		fmt.Printf("catalog: %s (%d verbs) OK\n", path, len(cat.ByName))
		return 0
	}

	tokFile := *tokenPath
	if !filepath.IsAbs(tokFile) {
		tokFile = filepath.Join(*dataDir, tokFile)
	}
	tok, err := auth.LoadOrCreate(tokFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: token: %v\n", err)
		return 1
	}

	aPath := *auditPath
	if aPath == "" {
		aPath = filepath.Join(*dataDir, "logs", "audit.log")
	}
	alog, err := audit.Open(aPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: audit: %v\n", err)
		return 1
	}
	defer alog.Close()

	dPath := *dbPath
	if dPath == "" {
		dPath = filepath.Join(*dataDir, "data", "tasks.db")
	}
	if err := os.MkdirAll(filepath.Dir(dPath), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: db dir: %v\n", err)
		return 1
	}
	store, err := queue.OpenSQLite(dPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: db: %v\n", err)
		return 1
	}
	defer store.Close()

	resumeStats, err := queue.Resume(store, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: resume: %v\n", err)
		return 1
	}

	circuits := circuit.NewRegistry(cat.Daemon.CBTripThreshold, time.Duration(cat.Daemon.CBOpenS)*time.Second)

	srv := api.New(cat, tok, store)
	srv.Version = Version
	srv.Audit = alog
	srv.SyncExec = *syncExec
	srv.Circuits = circuits
	srv.Resume = resumeStats
	srv.Streams = streams.NewRegistry(cat.Daemon.StreamBufferDefault)
	defer srv.Streams.CloseAll()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	w := &worker.Worker{
		Store:       store,
		Catalog:     cat,
		AuditLog:    alog,
		Notifier:    notify.Termux{},
		Circuits:    circuits,
		BackoffBase: time.Duration(cat.Daemon.BackoffBaseS * float64(time.Second)),
		MaxJitter:   250 * time.Millisecond,
		PollEvery:   200 * time.Millisecond,
	}
	go w.Run(ctx)

	fmt.Printf("dispatcher-go %s listening on http://%s\n", Version, cat.Daemon.Listen)
	fmt.Printf("catalog: %s (%d verbs)\n", path, len(cat.ByName))
	fmt.Printf("token: %s\n", tokFile)
	fmt.Printf("db: %s\n", dPath)
	fmt.Printf("audit: %s\n", aPath)
	fmt.Printf("resume: executing→pending=%d pending=%d retry_due=%d\n",
		resumeStats.ExecutingToPending, resumeStats.PendingKept, resumeStats.RetryDue)
	fmt.Printf("worker: on (sync-exec=%v)\n", *syncExec)
	if err := srv.ListenAndServe(ctx, cat.Daemon.Listen); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "dispatcher: serve: %v\n", err)
		return 1
	}
	return 0
}
