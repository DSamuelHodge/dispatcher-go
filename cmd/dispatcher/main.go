// Command dispatcher is the Termux:API loopback daemon entrypoint.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

// Version is set via -ldflags "-X main.Version=..." at release build time.
var Version = "0.1.0-dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("dispatcher", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	catalogPath := fs.String("catalog", "", "path to verbs.yaml (default: ./verbs.yaml)")
	validateOnly := fs.Bool("validate", false, "load and validate verbs.yaml then exit")
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

	fmt.Printf("dispatcher-go %s\n", Version)
	fmt.Printf("catalog: %s (%d verbs)\n", path, len(cat.ByName))
	fmt.Printf("listen: %s (not started — HTTP server lands in M2)\n", cat.Daemon.Listen)
	fmt.Printf("approval_mode: %s\n", cat.Daemon.ApprovalMode)

	if *validateOnly {
		fmt.Println("catalog: OK")
		return 0
	}

	// M1 complete: load + validate. M2 binds HTTP.
	fmt.Println("status: M1 loader ready; run with -validate to check catalog only")
	return 0
}
