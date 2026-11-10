// Command covercheck merges Go coverage profiles and enforces the committed
// coverage thresholds (test/coverage-thresholds.json).
//
// Profiles are merged block by block: a statement counts as covered when any
// profile covered it. This is how unit and integration coverage combine into
// the "overall" figure (see docs/adr/0010-coverage-measurement.md).
//
// Usage:
//
//	covercheck -thresholds test/coverage-thresholds.json unit.out integration.out
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Thresholds is the committed policy.
type Thresholds struct {
	// Module is the import path prefix stripped from file names.
	Module string `json:"module"`
	// Overall is the minimum percentage over every file whose path starts
	// with one of OverallPrefixes.
	Overall         float64  `json:"overall"`
	OverallPrefixes []string `json:"overall_prefixes"`
	// Files maps a file or directory prefix (relative to the module) to its
	// minimum percentage.
	Files map[string]float64 `json:"files"`
}

type block struct {
	stmts   int
	covered bool
}

func main() {
	thresholdsPath := flag.String("thresholds", "test/coverage-thresholds.json", "thresholds file")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: covercheck -thresholds FILE PROFILE...")
		os.Exit(2)
	}
	if err := run(*thresholdsPath, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "covercheck:", err)
		os.Exit(1)
	}
}

func run(thresholdsPath string, profiles []string) error {
	raw, err := os.ReadFile(thresholdsPath) //nolint:gosec // developer-supplied path
	if err != nil {
		return err
	}
	var th Thresholds
	if err := json.Unmarshal(raw, &th); err != nil {
		return fmt.Errorf("parse %s: %w", thresholdsPath, err)
	}
	blocks := map[string]map[string]*block{} // file -> block position -> block
	for _, p := range profiles {
		if err := readProfile(p, th.Module, blocks); err != nil {
			return err
		}
	}
	pct := func(match func(string) bool) (float64, int) {
		var total, covered int
		for file, bs := range blocks {
			if !match(file) {
				continue
			}
			for _, b := range bs {
				total += b.stmts
				if b.covered {
					covered += b.stmts
				}
			}
		}
		if total == 0 {
			return 0, 0
		}
		return 100 * float64(covered) / float64(total), total
	}
	var failures []string
	overall, n := pct(func(f string) bool {
		for _, prefix := range th.OverallPrefixes {
			if strings.HasPrefix(f, prefix) {
				return true
			}
		}
		return false
	})
	fmt.Printf("%-48s %6.1f%%  (minimum %.0f%%, %d statements)\n", "overall: "+strings.Join(th.OverallPrefixes, ", "), overall, th.Overall, n)
	if overall < th.Overall {
		failures = append(failures, fmt.Sprintf("overall coverage %.1f%% is below %.0f%%", overall, th.Overall))
	}
	names := make([]string, 0, len(th.Files))
	for name := range th.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got, n := pct(func(f string) bool { return strings.HasPrefix(f, name) })
		fmt.Printf("%-48s %6.1f%%  (minimum %.0f%%, %d statements)\n", name, got, th.Files[name], n)
		if n == 0 {
			failures = append(failures, name+" has no coverage data (renamed or untested?)")
		} else if got < th.Files[name] {
			failures = append(failures, fmt.Sprintf("%s coverage %.1f%% is below %.0f%%", name, got, th.Files[name]))
		}
	}
	if len(failures) > 0 {
		return errors.New("thresholds missed:\n  - " + strings.Join(failures, "\n  - "))
	}
	fmt.Println("all coverage thresholds met")
	return nil
}

// readProfile parses one text coverage profile ("mode: set|count|atomic"
// then "file:startLine.startCol,endLine.endCol numStmts count" lines).
func readProfile(path, module string, blocks map[string]map[string]*block) error {
	f, err := os.Open(path) //nolint:gosec // developer-supplied path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if line == 1 && strings.HasPrefix(text, "mode:") {
			continue
		}
		if text == "" {
			continue
		}
		colon := strings.LastIndex(text, ":")
		fields := strings.Fields(text[colon+1:])
		if colon < 0 || len(fields) != 3 {
			return fmt.Errorf("%s:%d: malformed profile line", path, line)
		}
		file := strings.TrimPrefix(strings.TrimPrefix(text[:colon], module), "/")
		stmts, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("%s:%d: malformed counts", path, line)
		}
		if blocks[file] == nil {
			blocks[file] = map[string]*block{}
		}
		b := blocks[file][fields[0]]
		if b == nil {
			b = &block{stmts: stmts}
			blocks[file][fields[0]] = b
		}
		b.covered = b.covered || count > 0
	}
	return sc.Err()
}
