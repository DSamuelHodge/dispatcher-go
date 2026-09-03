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

	"github.com/DSamuelHodge/dispatcher-go/internal/api"
	"github.com/DSamuelHodge/dispatcher-go/internal/approve"
	"github.com/DSamuelHodge/dispatcher-go/internal/audit"
	"github.com/DSamuelHodge/dispatcher-go/internal/auth"
	"github.com/DSamuelHodge/dispatcher-go/internal/queue"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Version is set via -ldflags "-X main.Version=..." at release build time.
var Version = "0.3.0-dev"

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
	policyPath := fs.String("policy-file", "", "approval-policy.json path (default: <data-dir>/.agent/approval-policy.json or $HOME/.agent/...)")
	auditPath := fs.String("audit-log", "", "NDJSON audit log path (default: <data-dir>/logs/audit.log)")
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

	polFile := *policyPath
	if polFile == "" {
		// prefer data-dir, then home
		cand := filepath.Join(*dataDir, ".agent", "approval-policy.json")
		if _, err := os.Stat(cand); err == nil {
			polFile = cand
		} else if home, err := os.UserHomeDir(); err == nil {
			polFile = filepath.Join(home, ".agent", "approval-policy.json")
		}
	}
	policy, err := approve.LoadPolicy(polFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatcher: policy: %v\n", err)
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

	tasks := queue.NewMemory()
	srv := api.New(cat, tok, tasks)
	srv.Policy = policy
	srv.Audit = alog
	srv.Prompter = approve.DialogPrompter{}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("dispatcher-go %s listening on http://%s\n", Version, cat.Daemon.Listen)
	fmt.Printf("catalog: %s (%d verbs)\n", path, len(cat.ByName))
	fmt.Printf("token: %s\n", tokFile)
	fmt.Printf("policy: %s (mode effective from merge)\n", polFile)
	fmt.Printf("audit: %s\n", aPath)
	if err := srv.ListenAndServe(ctx, cat.Daemon.Listen); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "dispatcher: serve: %v\n", err)
		return 1
	}
	return 0
}
