// Command load drives a constant request rate at a ponnoKhoj deployment and
// reports latency percentiles and errors (used by `make load` against kind;
// results are recorded in docs/architecture.md).
//
//	go run ./test/load -base-url http://localhost:8088 -rate 50 -duration 30s
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func main() {
	base := flag.String("base-url", "http://localhost:8088", "deployment to test")
	rate := flag.Int("rate", 50, "requests per second")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	district := flag.Int("district", 16, "district ID for the area searches (16 = Chattogram in the seed data)")
	p95Target := flag.Duration("p95", 300*time.Millisecond, "p95 latency target for local search")
	flag.Parse()

	b := strings.TrimRight(*base, "/")
	// A mix of local-search shapes: exact point, district-wide, and the HTML page.
	targets := []vegeta.Target{
		{Method: http.MethodGet, URL: b + "/api/v1/search?q=miniket+rice+5kg&mode=local&lat=22.335109&lng=91.834073&radius_m=3000"},
		{Method: http.MethodGet, URL: fmt.Sprintf("%s/api/v1/search?q=soybean+oil+1l&mode=local&district_id=%d", b, *district)},
		{Method: http.MethodGet, URL: b + "/api/v1/search?q=eggs+1+dozen&mode=local&lat=22.35&lng=91.82&radius_m=5000"},
		{Method: http.MethodGet, URL: fmt.Sprintf("%s/search?q=sugar&mode=local&district=%d", b, *district)},
	}
	attacker := vegeta.NewAttacker(vegeta.Timeout(10*time.Second), vegeta.KeepAlive(true))
	var m vegeta.Metrics
	for res := range attacker.Attack(vegeta.NewStaticTargeter(targets...), vegeta.Rate{Freq: *rate, Per: time.Second}, *duration, "ponnokhoj-load") {
		m.Add(res)
	}
	m.Close()
	fmt.Printf("requests: %d at %.1f/s for %s\n", m.Requests, m.Rate, *duration)
	fmt.Printf("latency: p50 %s  p95 %s  p99 %s  max %s\n", m.Latencies.P50.Round(time.Millisecond), m.Latencies.P95.Round(time.Millisecond), m.Latencies.P99.Round(time.Millisecond), m.Latencies.Max.Round(time.Millisecond))
	fmt.Printf("success: %.2f%%  status codes: %v\n", m.Success*100, m.StatusCodes)
	if len(m.Errors) > 0 {
		fmt.Printf("errors: %v\n", m.Errors[:min(len(m.Errors), 5)])
	}
	failed := false
	for code, n := range m.StatusCodes {
		// 429 counts too: a rate-limited run measures the limiter, not search.
		if strings.HasPrefix(code, "5") || code == "0" || code == "429" {
			fmt.Printf("FAIL: %d responses with status %s\n", n, code)
			failed = true
		}
	}
	if m.Latencies.P95 > *p95Target {
		fmt.Printf("FAIL: p95 %s exceeds the %s target\n", m.Latencies.P95, *p95Target)
		failed = true
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("load targets met")
}
