package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
	"golang.org/x/time/rate"
)

// BotName is the product token ponnoKhoj identifies as, in the User-Agent
// header and when reading robots.txt.
const BotName = "ponnoKhojBot"

// Fetch errors.
var (
	ErrRobotsDisallowed = errors.New("sources: disallowed by robots.txt")
	ErrCircuitOpen      = errors.New("sources: circuit breaker open")
	ErrBodyTooLarge     = errors.New("sources: response body exceeds the size cap")
	ErrBadURL           = errors.New("sources: URL must be absolute http(s)")
)

// StatusError is a non-2xx HTTP response.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("sources: HTTP %d", e.Code) }

// ErrorClass buckets an error for metrics and health reporting.
func ErrorClass(err error) string {
	var se *StatusError
	var ne net.Error
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrRobotsDisallowed):
		return "robots"
	case errors.Is(err, ErrCircuitOpen):
		return "circuit_open"
	case errors.Is(err, ErrBodyTooLarge):
		return "too_large"
	case errors.Is(err, ErrBadURL):
		return "bad_url"
	case errors.As(err, &se):
		switch {
		case se.Code == http.StatusTooManyRequests:
			return "http_429"
		case se.Code >= 500:
			return "http_5xx"
		default:
			return "http_4xx"
		}
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "network"
	}
}

// FetcherConfig configures a Fetcher. Zero values get conservative defaults.
type FetcherConfig struct {
	// ContactURL is included in the User-Agent so site owners can reach us.
	ContactURL string
	// MinInterval is the minimum spacing between requests to one host.
	MinInterval time.Duration
	// MaxBodyBytes caps how much of a response is read.
	MaxBodyBytes int64
	// MaxAttempts bounds tries per request (first try included).
	MaxAttempts int
	// RequestTimeout bounds one attempt end to end.
	RequestTimeout time.Duration
	// RobotsTTL is how long a robots.txt is cached.
	RobotsTTL time.Duration
	// BreakerThreshold and BreakerCooldown configure the circuit breaker.
	BreakerThreshold int
	BreakerCooldown  time.Duration
	// MaxRetryAfter is the longest Retry-After the fetcher will wait out.
	MaxRetryAfter time.Duration
}

func (c FetcherConfig) withDefaults() FetcherConfig {
	if c.MinInterval <= 0 {
		c.MinInterval = 2 * time.Second
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 2 << 20
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 20 * time.Second
	}
	if c.RobotsTTL <= 0 {
		c.RobotsTTL = time.Hour
	}
	if c.BreakerThreshold <= 0 {
		c.BreakerThreshold = 5
	}
	if c.BreakerCooldown <= 0 {
		c.BreakerCooldown = time.Minute
	}
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = 30 * time.Second
	}
	return c
}

// Fetcher is the shared, polite HTTP GET client. Each online source owns its
// own Fetcher, so no cookies, connections or breaker state are shared across
// sources. It:
//
//   - obeys robots.txt (cached), failing closed when robots.txt is unreachable;
//   - sends an identifying User-Agent with a contact URL;
//   - rate-limits per host with a token bucket;
//   - applies connect, header and whole-request timeouts and a body size cap;
//   - retries only network errors, 429 and 5xx, with jittered exponential
//     backoff, honoring Retry-After;
//   - trips a circuit breaker after repeated failures;
//   - never stores cookies and refuses cross-host redirects.
type Fetcher struct {
	cfg       FetcherConfig
	client    *http.Client
	userAgent string
	breaker   *Breaker
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	robots   map[string]robotsEntry
}

type robotsEntry struct {
	data      *robotstxt.RobotsData
	fetchedAt time.Time
}

// NewFetcher builds a Fetcher with its own transport.
func NewFetcher(cfg FetcherConfig) *Fetcher {
	cfg = cfg.withDefaults()
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
	}
	ua := BotName + "/1.0"
	if cfg.ContactURL != "" {
		ua += " (+" + cfg.ContactURL + ")"
	}
	return &Fetcher{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			// No Jar: cookies are never stored or replayed.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("sources: too many redirects")
				}
				if req.URL.Host != via[0].URL.Host {
					// A redirect to another host would skip that host's
					// robots.txt and rate limit.
					return errors.New("sources: cross-host redirect refused")
				}
				return nil
			},
		},
		userAgent: ua,
		breaker:   NewBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
		now:       time.Now,
		sleep:     sleepCtx,
		limiters:  map[string]*rate.Limiter{},
		robots:    map[string]robotsEntry{},
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// UserAgent returns the User-Agent header the fetcher sends.
func (f *Fetcher) UserAgent() string { return f.userAgent }

func (f *Fetcher) limiter(host string) *rate.Limiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.limiters[host]
	if !ok {
		l = rate.NewLimiter(rate.Every(f.cfg.MinInterval), 1)
		f.limiters[host] = l
	}
	return l
}

// Get fetches rawURL and returns the body.
func (f *Fetcher) Get(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrBadURL
	}
	if !f.breaker.Allow() {
		return nil, ErrCircuitOpen
	}
	body, err := f.get(ctx, u)
	// robots.txt saying no is the site working as intended, not a failure.
	f.breaker.Record(err == nil || errors.Is(err, ErrRobotsDisallowed))
	return body, err
}

func (f *Fetcher) get(ctx context.Context, u *url.URL) ([]byte, error) {
	allowed, err := f.robotsAllowed(ctx, u)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrRobotsDisallowed
	}
	return f.doWithRetry(ctx, u.String())
}

func (f *Fetcher) robotsAllowed(ctx context.Context, u *url.URL) (bool, error) {
	origin := u.Scheme + "://" + u.Host
	f.mu.Lock()
	entry, ok := f.robots[origin]
	f.mu.Unlock()
	if !ok || f.now().Sub(entry.fetchedAt) > f.cfg.RobotsTTL {
		data, err := f.fetchRobots(ctx, origin)
		if err != nil {
			return false, err
		}
		entry = robotsEntry{data: data, fetchedAt: f.now()}
		f.mu.Lock()
		f.robots[origin] = entry
		f.mu.Unlock()
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if path == "" {
		path = "/"
	}
	return entry.data.TestAgent(path, BotName), nil
}

// fetchRobots loads and parses robots.txt following RFC 9309: a 4xx means no
// restrictions; a 5xx or network failure means the site is off-limits.
func (f *Fetcher) fetchRobots(ctx context.Context, origin string) (*robotstxt.RobotsData, error) {
	status, body, _, err := f.do(ctx, origin+"/robots.txt")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		status, body = http.StatusServiceUnavailable, nil // unreachable: disallow all
	}
	data, err := robotstxt.FromStatusAndBytes(status, body)
	if err != nil {
		// Unparsable robots.txt: fail closed.
		data, err = robotstxt.FromStatusAndBytes(http.StatusServiceUnavailable, nil)
		if err != nil {
			return nil, fmt.Errorf("sources: robots: %w", err)
		}
	}
	return data, nil
}

func (f *Fetcher) doWithRetry(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= f.cfg.MaxAttempts; attempt++ {
		status, body, retryAfter, err := f.do(ctx, rawURL)
		switch {
		case err == nil && status >= 200 && status < 300:
			return body, nil
		case err == nil:
			lastErr = &StatusError{Code: status}
			if status != http.StatusTooManyRequests && status < 500 {
				return nil, lastErr // 4xx: retrying will not help
			}
		case errors.Is(err, ErrBodyTooLarge) || ctx.Err() != nil:
			return nil, err
		default:
			lastErr = err
		}
		if attempt == f.cfg.MaxAttempts {
			break
		}
		wait := backoff(attempt)
		if retryAfter > 0 {
			if retryAfter > f.cfg.MaxRetryAfter {
				return nil, lastErr // the site asked for a long pause: respect it by stopping
			}
			wait = retryAfter
		}
		if err := f.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// backoff is exponential (0.5 s, 1 s, 2 s, ...) with full jitter.
func backoff(attempt int) time.Duration {
	base := 500 * time.Millisecond << min(attempt-1, 6)
	return time.Duration(rand.Int64N(int64(base))) + base/2 //nolint:gosec // jitter does not need a CSPRNG
}

// do performs one rate-limited GET. It returns the status, the body (capped)
// and any Retry-After delay.
func (f *Fetcher) do(ctx context.Context, rawURL string) (status int, body []byte, retryAfter time.Duration, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, 0, ErrBadURL
	}
	if err := f.limiter(u.Host).Wait(ctx); err != nil {
		return 0, nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, 0, ErrBadURL
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,text/plain;q=0.8")
	req.Header.Set("Accept-Language", "en,bn;q=0.9")
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), f.now())
	body, err = io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBodyBytes+1))
	if err != nil {
		return resp.StatusCode, nil, retryAfter, err
	}
	if int64(len(body)) > f.cfg.MaxBodyBytes {
		return resp.StatusCode, nil, retryAfter, ErrBodyTooLarge
	}
	return resp.StatusCode, body, retryAfter, nil
}

// parseRetryAfter understands both forms of the header: delay seconds and an
// HTTP date. It returns 0 when the header is absent or unusable.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(min(secs, 86400)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
