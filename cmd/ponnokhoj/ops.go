package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shtmthw/ponnoKhoj/internal/auth"
	"github.com/shtmthw/ponnoKhoj/internal/domain"
	"github.com/shtmthw/ponnoKhoj/internal/estimate"
	"github.com/shtmthw/ponnoKhoj/internal/llm"
	"github.com/shtmthw/ponnoKhoj/internal/migrations"
	"github.com/shtmthw/ponnoKhoj/internal/seed"
	"github.com/shtmthw/ponnoKhoj/internal/store"
)

func cmdMigrate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: ponnokhoj migrate up|down|version   (down --all rolls everything back)")
	}
	dbURL, err := databaseURL()
	if err != nil {
		return err
	}
	switch args[0] {
	case "up":
		res, err := migrations.Up(ctx, dbURL)
		for _, r := range res {
			_, _ = fmt.Fprintf(stdout, "applied %05d %s (%s)\n", r.Version, r.Name, r.Duration.Round(1e6))
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "schema is at version %d\n", migrations.ExpectedVersion())
	case "down":
		all := len(args) > 1 && args[1] == "--all"
		res, err := migrations.Down(ctx, dbURL, all)
		for _, r := range res {
			_, _ = fmt.Fprintf(stdout, "rolled back %05d %s (%s)\n", r.Version, r.Name, r.Duration.Round(1e6))
		}
		if err != nil {
			return err
		}
	case "version":
		v, err := migrations.Version(ctx, dbURL)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "database schema version: %d (this binary expects %d)\n", v, migrations.ExpectedVersion())
	default:
		return fmt.Errorf("unknown migrate action %q", args[0])
	}
	return nil
}

func cmdSeed(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	demo := fs.Bool("demo", false, "also load demo data (fictional shops and prices); refused when PONNOKHOJ_ENV=prod")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dbURL, err := databaseURL()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbURL, 4)
	if err != nil {
		return err
	}
	defer st.Close()

	n, err := seed.Areas(ctx, st)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "areas: %d loaded (source: nuhil/bangladesh-geocode, MIT)\n", n)
	cats, err := seed.Categories(ctx, st)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "categories: %d loaded\n", len(cats))
	products, err := seed.Catalog(ctx, st, cats)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "reference catalog: %d real products with their online product pages\n", products)
	if !*demo {
		return nil
	}
	if os.Getenv("PONNOKHOJ_ENV") == "prod" {
		return errors.New("demo data is never loaded in prod (PONNOKHOJ_ENV=prod)")
	}
	adminPw, vendorPw := os.Getenv("SEED_ADMIN_PASSWORD"), os.Getenv("SEED_VENDOR_PASSWORD")
	if auth.CheckPasswordPolicy(adminPw) != nil || auth.CheckPasswordPolicy(vendorPw) != nil {
		return errors.New("seed --demo needs SEED_ADMIN_PASSWORD and SEED_VENDOR_PASSWORD (at least 10 characters each); they are never hardcoded")
	}
	authSvc, err := auth.NewService(st, auth.Config{})
	if err != nil {
		return err
	}
	sum, err := seed.Demo(ctx, st, authSvc, seed.DemoOptions{AdminPassword: adminPw, VendorPassword: vendorPw})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "demo data: %d products, %d shops, %d listings (all flagged is_demo)\n", sum.Products, sum.Shops, sum.Listings)
	_, _ = fmt.Fprintf(stdout, "demo accounts: %s (admin), %s (vendor); passwords are the SEED_*_PASSWORD values you supplied\n", seed.DemoAdminEmail, seed.DemoVendorEmail)
	return nil
}

func cmdEstimate(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 1 || args[0] != "eval" {
		return errors.New("usage: ponnokhoj estimate eval --dataset FILE [--thresholds FILE] [--llm]")
	}
	fs := flag.NewFlagSet("estimate eval", flag.ContinueOnError)
	datasetPath := fs.String("dataset", "test/fixtures/eval/dataset.json", "evaluation dataset (JSON)")
	thresholdsPath := fs.String("thresholds", "", "thresholds file (JSON); when set, a miss is an error")
	radius := fs.Int("radius", 3000, "search radius in meters")
	useLLM := fs.Bool("llm", false, "also evaluate the LLM-assisted path (needs LLM_BASE_URL and LLM_MODEL; sends the dataset's evidence to that endpoint)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ds, err := estimate.LoadEvalDataset(*datasetPath)
	if err != nil {
		return err
	}
	params := estimate.DefaultParams()
	base, err := estimate.Backtest(ctx, ds, params, *radius, nil)
	if err != nil {
		return err
	}
	printReport(stdout, "statistical baseline", base)

	if *useLLM {
		baseURL, model := os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_MODEL")
		if baseURL == "" || model == "" {
			return errors.New("--llm needs LLM_BASE_URL and LLM_MODEL")
		}
		agent := estimate.NewAgent(llm.NewHTTPClient(baseURL, os.Getenv("LLM_API_KEY")), estimate.AgentConfig{Model: model, MaxSteps: 6, Timeout: 60e9, MaxTokens: 1024})
		fallbacks := 0
		report, err := estimate.Backtest(ctx, ds, params, *radius, func(ctx context.Context, in estimate.AgentInput) (estimate.Result, error) {
			run, err := agent.Refine(ctx, in)
			if err != nil {
				fallbacks++
				return estimate.Result{}, err
			}
			return estimate.Result{
				Low: domain.Money(run.Output.Low), Point: domain.Money(run.Output.Point), High: domain.Money(run.Output.High),
				Confidence: estimate.MinConfidence(run.Output.Confidence, in.Baseline.Confidence),
			}, nil
		})
		if err != nil {
			return err
		}
		printReport(stdout, "LLM-assisted ("+model+")", report)
		_, _ = fmt.Fprintf(stdout, "  fell back to the baseline: %d of %d\n", fallbacks, report.Estimated)
	}

	if *thresholdsPath != "" {
		th, err := estimate.LoadEvalThresholds(*thresholdsPath)
		if err != nil {
			return err
		}
		if failures := th.Check(base); len(failures) > 0 {
			return fmt.Errorf("baseline misses the committed thresholds:\n  - %s", strings.Join(failures, "\n  - "))
		}
		_, _ = fmt.Fprintln(stdout, "baseline meets the committed thresholds")
	}
	return nil
}

func printReport(w io.Writer, name string, r estimate.EvalReport) {
	raw, _ := json.Marshal(r) // a struct of numbers always encodes
	_, _ = fmt.Fprintf(w, "%s:\n  held-out listings: %d, estimated: %d (%.1f%%)\n  MAPE: %.2f%%\n  median absolute error: ৳%s\n  range coverage: %.1f%%\n  json: %s\n",
		name, r.Listings, r.Estimated, estimate.RoundPct(r.EstimatedShare()), r.MAPE*100,
		domain.Money(r.MedianAbsErrorPoisha).Decimal(), estimate.RoundPct(r.RangeCoverage), raw)
}
