// Command ponnokhoj is the single ponnoKhoj binary. Its subcommands run
// every workload: the web server, the background worker, migrations, the
// seeder, a healthcheck for container runtimes, and operator utilities.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo; embed it

	"github.com/shtmthw/ponnoKhoj/internal/config"
	"github.com/shtmthw/ponnoKhoj/internal/version"
)

const usage = `ponnokhoj — find the best price online, at local shops, or by estimate.

Usage:
  ponnokhoj serve                      run the web UI and JSON API
  ponnokhoj worker                     run background jobs (River)
  ponnokhoj migrate up|down|version    apply or roll back schema migrations
  ponnokhoj seed [--demo]              load areas and categories (and demo data)
  ponnokhoj healthcheck [--url URL]    probe a running server's /readyz
  ponnokhoj config check               validate and print the configuration
  ponnokhoj estimate eval [flags]      backtest the price estimator
  ponnokhoj version                    print build information

Configuration is read from environment variables; see README.md.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch args[0] {
	case "serve":
		err = cmdServe(ctx, stderr)
	case "worker":
		err = cmdWorker(ctx, stderr)
	case "migrate":
		err = cmdMigrate(ctx, args[1:], stdout)
	case "seed":
		err = cmdSeed(ctx, args[1:], stdout)
	case "healthcheck":
		err = cmdHealthcheck(ctx, args[1:], stdout)
	case "config":
		err = cmdConfig(args[1:], stdout)
	case "estimate":
		err = cmdEstimate(ctx, args[1:], stdout)
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, version.String())
	case "help", "--help", "-h":
		_, _ = fmt.Fprint(stdout, usage)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// databaseURL reads DATABASE_URL for the commands that need nothing else.
func databaseURL() (string, error) {
	v := os.Getenv("DATABASE_URL")
	if v == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	return v, nil
}

func cmdConfig(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "check" {
		return errors.New("usage: ponnokhoj config check")
	}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "configuration is valid; effective values (secrets redacted):")
	for _, line := range cfg.Redacted(os.LookupEnv) {
		_, _ = fmt.Fprintln(stdout, "  "+line)
	}
	return nil
}

// cmdHealthcheck exists because the distroless image has no curl or wget.
func cmdHealthcheck(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8080/readyz", "URL to probe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s returned HTTP %d", *url, resp.StatusCode)
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return nil
}
