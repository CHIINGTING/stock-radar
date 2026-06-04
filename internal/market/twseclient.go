package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TwseConfig configures a TwseClient. Use DefaultTwseConfig and override fields.
type TwseConfig struct {
	Timeout             time.Duration
	DisableKeepAlives   bool
	ForceHTTP1          bool // disable HTTP/2 (h2 is a common MIS reset source)
	UserAgent           string
	Referer             string
	Accept              string
	MaxIdleConnsPerHost int
	MaxConcurrent       int             // global in-flight request cap (semaphore)
	MaxRetries          int             // retries after the first attempt
	Backoffs            []time.Duration // wait before retry 1, 2, 3
	JitterMax           time.Duration   // extra random delay added to each backoff
}

// DefaultTwseConfig returns settings tuned to avoid TWSE MIS connection resets:
// low concurrency, HTTP/1.1, browser-like headers, exponential backoff with jitter.
func DefaultTwseConfig() TwseConfig {
	return TwseConfig{
		Timeout:             10 * time.Second,
		DisableKeepAlives:   false,
		ForceHTTP1:          true,
		UserAgent:           "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
		Referer:             "https://mis.twse.com.tw/stock/index.jsp",
		Accept:              "application/json, text/javascript, */*; q=0.01",
		MaxIdleConnsPerHost: 4,
		MaxConcurrent:       3,
		MaxRetries:          3,
		Backoffs:            []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond},
		JitterMax:           300 * time.Millisecond,
	}
}

// TwseClient is a hardened TWSE MIS realtime client. It satisfies RealtimeProvider.
//
//   - A single shared, configurable http.Client.
//   - A global semaphore caps concurrent outbound requests.
//   - Per-code single-flight collapses duplicate concurrent refreshes of one code.
//   - Retries only transient failures (reset / EOF / timeout) with exp backoff + jitter.
//   - On failure the last good quote is retained and served (the cache is never cleared).
//   - Every attempt logs: code, attempt, latency, error type, final result.
type TwseClient struct {
	cfg     TwseConfig
	http    *http.Client
	sem     chan struct{}
	hints   map[string]string // code → "tse" | "otc"
	baseURL string            // MIS endpoint; overridable in tests

	group *singleFlight

	lastMu sync.RWMutex
	last   map[string]*Quote
}

const misBaseURL = "https://mis.twse.com.tw/stock/api/getStockInfo.jsp"

// NewTwseClient builds a client. hints maps codes to their known MIS market
// ("tse"/"otc") to skip auto-detection; pass nil to always auto-detect.
func NewTwseClient(cfg TwseConfig, hints map[string]string) *TwseClient {
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	return &TwseClient{
		cfg:     cfg,
		http:    buildHTTPClient(cfg.Timeout, cfg.ForceHTTP1, cfg.DisableKeepAlives, cfg.MaxIdleConnsPerHost),
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		hints:   hints,
		baseURL: misBaseURL,
		group:   newSingleFlight(),
		last:    make(map[string]*Quote),
	}
}

// GetQuote fetches the realtime quote for code. Concurrent calls for the same
// code share one in-flight request. On failure the last good quote is returned
// (if any) so callers never lose data; only a first-ever failure surfaces an error.
func (c *TwseClient) GetQuote(code string) (*Quote, error) {
	q, err := c.group.Do(code, func() (*Quote, error) {
		return c.fetchResolved(code)
	})
	if err != nil {
		if last := c.lastGood(code); last != nil {
			log.Printf("twse code=%s final=STALE-CACHE (refresh failed: %v)", code, err)
			cp := *last
			return &cp, nil
		}
		log.Printf("twse code=%s final=FAILED (no cache): %v", code, err)
		return nil, err
	}
	c.setLast(code, q)
	return q, nil
}

// fetchResolved resolves the market (hint or auto-detect tse→otc) and fetches.
func (c *TwseClient) fetchResolved(code string) (*Quote, error) {
	if mkt := c.hints[code]; mkt != "" {
		return c.fetchMarket(code, mkt)
	}
	q, err := c.fetchMarket(code, "tse")
	if err == nil {
		return q, nil
	}
	// Only probe OTC when TSE definitively has no such symbol; never on transient.
	if !errors.Is(err, errStockNotOnMarket) {
		return nil, err
	}
	return c.fetchMarket(code, "otc")
}

// fetchMarket performs the retry loop for one market. Only transient network
// errors (reset / eof / timeout) are retried.
func (c *TwseClient) fetchMarket(code, market string) (*Quote, error) {
	url := fmt.Sprintf("%s?ex_ch=%s_%s.tw", c.baseURL, market, code)
	attempts := c.cfg.MaxRetries + 1
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		q, kind, lat, err := c.doOnce(url)
		log.Printf("twse code=%s market=%s attempt=%d/%d latency=%s type=%s",
			code, market, attempt, attempts, lat.Round(time.Millisecond), kind)

		switch kind {
		case "success":
			return q, nil
		case "not_found":
			return nil, fmt.Errorf("stock %s not on %s: %w", code, market, errStockNotOnMarket)
		case "reset", "eof", "timeout":
			lastErr = err
			if i := attempt - 1; attempt <= c.cfg.MaxRetries && i < len(c.cfg.Backoffs) {
				time.Sleep(c.cfg.Backoffs[i] + c.jitter())
				continue
			}
			// retries exhausted → fall through
		default: // http_status, json_error, build_error — not retryable
			return nil, fmt.Errorf("twse %s/%s: %s: %w", code, market, kind, err)
		}
	}
	return nil, fmt.Errorf("twse %s/%s after %d attempts: %w", code, market, attempts, lastErr)
}

// doOnce issues one HTTP request under the global semaphore, classifies the
// outcome, records metrics, and parses the quote on success.
func (c *TwseClient) doOnce(url string) (*Quote, string, time.Duration, error) {
	c.sem <- struct{}{} // global concurrency cap
	defer func() { <-c.sem }()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "build_error", 0, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Referer", c.cfg.Referer)
	req.Header.Set("Accept", c.cfg.Accept)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	start := time.Now()
	resp, err := c.http.Do(req)
	lat := time.Since(start)
	if err != nil {
		kind := classifyNetErr(err)
		GlobalMetrics.record(false, kind == "eof", false, lat)
		return nil, kind, lat, err
	}
	defer resp.Body.Close()

	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		kind := classifyNetErr(rerr)
		GlobalMetrics.record(false, kind == "eof", false, lat)
		return nil, kind, lat, rerr
	}
	if resp.StatusCode != http.StatusOK {
		GlobalMetrics.record(false, false, false, lat)
		return nil, "http_status", lat, fmt.Errorf("status %d", resp.StatusCode)
	}

	var result misResponse
	if err := json.Unmarshal(body, &result); err != nil {
		GlobalMetrics.record(false, false, false, lat)
		return nil, "json_error", lat, err
	}
	if len(result.MsgArray) == 0 || strings.TrimSpace(result.MsgArray[0].Code) == "" {
		GlobalMetrics.record(true, false, false, lat) // a valid "not on this market" response
		return nil, "not_found", lat, nil
	}

	GlobalMetrics.record(true, false, false, lat)
	return quoteFromMIS(result.MsgArray[0]), "success", lat, nil
}

func (c *TwseClient) jitter() time.Duration {
	if c.cfg.JitterMax <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(c.cfg.JitterMax)))
}

func (c *TwseClient) lastGood(code string) *Quote {
	c.lastMu.RLock()
	defer c.lastMu.RUnlock()
	return c.last[code]
}

func (c *TwseClient) setLast(code string, q *Quote) {
	c.lastMu.Lock()
	c.last[code] = q
	c.lastMu.Unlock()
}

// classifyNetErr maps a transport error to a stable type label.
func classifyNetErr(err error) string {
	if err == nil {
		return "success"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "reset"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	if isEOF(err) {
		return "eof"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "connection reset by peer"):
		return "reset"
	case strings.Contains(s, "Client.Timeout") || strings.Contains(s, "i/o timeout") || strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "connection refused"):
		return "refused"
	}
	return "other"
}

// ── single-flight: collapse concurrent requests for the same key ────────────

type singleFlight struct {
	mu sync.Mutex
	m  map[string]*sfCall
}

type sfCall struct {
	wg  sync.WaitGroup
	val *Quote
	err error
}

func newSingleFlight() *singleFlight { return &singleFlight{m: make(map[string]*sfCall)} }

// Do runs fn for key, ensuring only one execution is in flight at a time;
// concurrent callers for the same key wait and receive the shared result.
func (g *singleFlight) Do(key string, fn func() (*Quote, error)) (*Quote, error) {
	g.mu.Lock()
	if call, ok := g.m[key]; ok {
		g.mu.Unlock()
		call.wg.Wait()
		return call.val, call.err
	}
	call := &sfCall{}
	call.wg.Add(1)
	g.m[key] = call
	g.mu.Unlock()

	call.val, call.err = fn()
	call.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
	return call.val, call.err
}
