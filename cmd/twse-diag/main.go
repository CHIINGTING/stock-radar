// Command twse-diag is a reproducible diagnostic harness for the TWSE MIS API.
//
// It hammers https://mis.twse.com.tw/stock/api/getStockInfo.jsp under a matrix of
// conditions (concurrency, interval, headers, keep-alive, HTTP version, session
// cookie, retry policy) and reports per-scenario statistics — so "connection
// reset by peer" can be correlated with a specific factor instead of guessed at.
//
// Usage:
//
//	go run ./cmd/twse-diag                          # run the full matrix A–G
//	go run ./cmd/twse-diag -scenario C -rounds 5    # one scenario, more rounds
//	go run ./cmd/twse-diag -codes 2324,3481,2330    # custom codes
//	go run ./cmd/twse-diag -v                        # per-request logging
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	apiURL   = "https://mis.twse.com.tw/stock/api/getStockInfo.jsp?ex_ch=%s_%s.tw"
	indexURL = "https://mis.twse.com.tw/stock/index.jsp"

	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
	referer   = "https://mis.twse.com.tw/stock/index.jsp"
	accept    = "application/json, text/javascript, */*; q=0.01"
)

// scenario describes one row of the test matrix.
type scenario struct {
	id          string
	name        string
	concurrency int
	interval    time.Duration // gap between request launches
	timeout     time.Duration
	withHeaders bool
	keepAlive   bool // true = reuse connections
	forceHTTP1  bool
	session     bool          // warm a cookie via index.jsp first
	idlePerHost int           // MaxIdleConnsPerHost (0 = default)
	retry       bool          // scenario G: exp backoff retry on transient errors
	backoffs    []time.Duration
}

func matrix() []scenario {
	return []scenario{
		{id: "A", name: "baseline (c1, 1000ms, default client)",
			concurrency: 1, interval: 1000 * time.Millisecond, timeout: 15 * time.Second,
			withHeaders: false, keepAlive: true},
		{id: "B", name: "normal radar (c3, 500ms, UA+Referer)",
			concurrency: 3, interval: 500 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: true},
		{id: "C", name: "aggressive (c10, 100ms)",
			concurrency: 10, interval: 100 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: true},
		{id: "D", name: "no keep-alive (DisableKeepAlives)",
			concurrency: 3, interval: 500 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: false},
		{id: "E", name: "force HTTP/1.1",
			concurrency: 3, interval: 500 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: true, forceHTTP1: true},
		{id: "F", name: "session mode (warm index.jsp cookie)",
			concurrency: 3, interval: 500 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: true, session: true},
		{id: "G", name: "safe production (c3, jitter, exp backoff retry)",
			concurrency: 3, interval: 400 * time.Millisecond, timeout: 10 * time.Second,
			withHeaders: true, keepAlive: true, forceHTTP1: true, idlePerHost: 4, retry: true,
			backoffs: []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond}},
	}
}

var verbose bool

func main() {
	scenarioFlag := flag.String("scenario", "all", "scenario id (A–G) or 'all'")
	rounds := flag.Int("rounds", 3, "passes over the code list per scenario")
	codesFlag := flag.String("codes", "2324,3481,2330,2317,2454", "comma-separated stock codes")
	market := flag.String("market", "tse", "MIS market: tse or otc")
	flag.BoolVar(&verbose, "v", false, "log every request")
	flag.Parse()

	codes := splitCodes(*codesFlag)
	want := strings.ToUpper(*scenarioFlag)

	var runs []scenario
	for _, sc := range matrix() {
		if want == "ALL" || want == sc.id {
			runs = append(runs, sc)
		}
	}
	if len(runs) == 0 {
		log.Fatalf("unknown scenario %q", *scenarioFlag)
	}

	fmt.Printf("TWSE MIS diagnostic — codes=%v market=%s rounds=%d\n\n", codes, *market, *rounds)

	results := make(map[string]*stats)
	for _, sc := range runs {
		fmt.Printf("── Scenario %s: %s ──\n", sc.id, sc.name)
		st := runScenario(sc, codes, *market, *rounds)
		st.print()
		results[sc.id] = st
		fmt.Println()
		// Brief pause between scenarios so one does not poison the next.
		time.Sleep(2 * time.Second)
	}

	if len(runs) > 1 {
		printComparison(runs, results)
	}
}

// runScenario executes one scenario and returns aggregated stats.
func runScenario(sc scenario, codes []string, market string, rounds int) *stats {
	tr := &http.Transport{
		DisableKeepAlives:   !sc.keepAlive,
		ForceAttemptHTTP2:   !sc.forceHTTP1,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: sc.idlePerHost,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if sc.forceHTTP1 {
		// A non-nil empty map disables the automatic HTTP/2 upgrade.
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}

	var jar http.CookieJar
	if sc.session {
		jar, _ = cookiejar.New(nil)
	}
	client := &http.Client{Timeout: sc.timeout, Transport: tr, Jar: jar}

	if sc.session {
		warmSession(client, sc)
	}

	// Build the job list: `rounds` passes over the codes.
	var jobs []string
	for r := 0; r < rounds; r++ {
		jobs = append(jobs, codes...)
	}

	st := newStats()
	sem := make(chan struct{}, sc.concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, code := range jobs {
		time.Sleep(sc.interval) // pace launches
		sem <- struct{}{}
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			defer func() { <-sem }()

			status, lat, kind := doRequest(client, sc, code, market)
			mu.Lock()
			st.add(code, status, lat, kind)
			mu.Unlock()
			if verbose {
				log.Printf("[%s] code=%s status=%d latency=%s type=%s",
					sc.id, code, status, lat.Round(time.Millisecond), kind)
			}
		}(code)
	}
	wg.Wait()
	return st
}

// doRequest performs one logical fetch, applying scenario G's retry policy.
func doRequest(client *http.Client, sc scenario, code, market string) (int, time.Duration, string) {
	status, lat, kind := fetchOnce(client, sc, code, market)
	if !sc.retry {
		return status, lat, kind
	}
	// Retry only on transient network errors, with exponential backoff + jitter.
	total := lat
	for attempt := 0; attempt < len(sc.backoffs) && isTransient(kind); attempt++ {
		sleep := sc.backoffs[attempt] + time.Duration(rand.Intn(300))*time.Millisecond
		time.Sleep(sleep)
		status, lat, kind = fetchOnce(client, sc, code, market)
		total += lat
	}
	return status, total, kind
}

// fetchOnce issues a single HTTP GET and classifies the outcome.
func fetchOnce(client *http.Client, sc scenario, code, market string) (int, time.Duration, string) {
	url := fmt.Sprintf(apiURL, market, code)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, "build_error"
	}
	if sc.withHeaders {
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Referer", referer)
		req.Header.Set("Accept", accept)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}

	start := time.Now()
	resp, err := client.Do(req)
	lat := time.Since(start)
	if err != nil {
		return 0, lat, classifyErr(err)
	}
	defer resp.Body.Close()

	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return resp.StatusCode, lat, classifyErr(rerr)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, lat, "http_status"
	}

	var parsed struct {
		MsgArray []struct {
			Code string `json:"c"`
		} `json:"msgArray"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return resp.StatusCode, lat, "json_error"
	}
	if len(parsed.MsgArray) == 0 || strings.TrimSpace(parsed.MsgArray[0].Code) == "" {
		return resp.StatusCode, lat, "empty_result"
	}
	return resp.StatusCode, lat, "success"
}

// warmSession fetches index.jsp once so the cookie jar carries a session cookie.
func warmSession(client *http.Client, sc scenario) {
	req, _ := http.NewRequest(http.MethodGet, indexURL, nil)
	if sc.withHeaders {
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[%s] session warm-up failed: %v", sc.id, err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if u, _ := req.URL.Parse(indexURL); client.Jar != nil {
		log.Printf("[%s] session warmed, cookies=%d", sc.id, len(client.Jar.Cookies(u)))
	}
}

// ── error classification ────────────────────────────────────────────────────

func classifyErr(err error) string {
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
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
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
	case strings.Contains(s, "EOF"):
		return "eof"
	case strings.Contains(s, "no such host"):
		return "dns"
	case strings.Contains(s, "proxyconnect") || strings.Contains(s, "proxy"):
		return "proxy"
	}
	return "other"
}

func isTransient(kind string) bool {
	switch kind {
	case "reset", "eof", "timeout":
		return true
	}
	return false
}

// ── statistics ──────────────────────────────────────────────────────────────

type stats struct {
	total     int
	success   int
	reset     int
	timeout   int
	eof       int
	non200    int
	jsonErr   int
	empty     int
	other     int
	latencies []time.Duration
	perCode   map[string]*codeStat
}

type codeStat struct{ total, errs int }

func newStats() *stats { return &stats{perCode: map[string]*codeStat{}} }

func (s *stats) add(code string, status int, lat time.Duration, kind string) {
	s.total++
	if lat > 0 {
		s.latencies = append(s.latencies, lat)
	}
	cs := s.perCode[code]
	if cs == nil {
		cs = &codeStat{}
		s.perCode[code] = cs
	}
	cs.total++

	switch kind {
	case "success":
		s.success++
	case "reset":
		s.reset++
		cs.errs++
	case "timeout":
		s.timeout++
		cs.errs++
	case "eof":
		s.eof++
		cs.errs++
	case "http_status":
		s.non200++
		cs.errs++
	case "json_error":
		s.jsonErr++
		cs.errs++
	case "empty_result":
		s.empty++
		cs.errs++
	default:
		s.other++
		cs.errs++
	}
}

func (s *stats) avg() time.Duration {
	if len(s.latencies) == 0 {
		return 0
	}
	var sum time.Duration
	for _, l := range s.latencies {
		sum += l
	}
	return sum / time.Duration(len(s.latencies))
}

func (s *stats) p95() time.Duration {
	n := len(s.latencies)
	if n == 0 {
		return 0
	}
	sorted := make([]time.Duration, n)
	copy(sorted, s.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(0.95 * float64(n-1))
	return sorted[idx]
}

func (s *stats) successRate() float64 {
	if s.total == 0 {
		return 0
	}
	return float64(s.success) / float64(s.total) * 100
}

func (s *stats) print() {
	fmt.Printf("  total=%d  success=%d (%.0f%%)  reset=%d  timeout=%d  eof=%d  non200=%d  json_err=%d  empty=%d  other=%d\n",
		s.total, s.success, s.successRate(), s.reset, s.timeout, s.eof, s.non200, s.jsonErr, s.empty, s.other)
	fmt.Printf("  latency: avg=%s  p95=%s\n", s.avg().Round(time.Millisecond), s.p95().Round(time.Millisecond))

	codes := make([]string, 0, len(s.perCode))
	for c := range s.perCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	fmt.Printf("  per-code error rate: ")
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		cs := s.perCode[c]
		rate := 0.0
		if cs.total > 0 {
			rate = float64(cs.errs) / float64(cs.total) * 100
		}
		parts = append(parts, fmt.Sprintf("%s=%.0f%%", c, rate))
	}
	fmt.Println(strings.Join(parts, "  "))
}

func printComparison(runs []scenario, results map[string]*stats) {
	fmt.Println("══ Comparison ══")
	fmt.Printf("%-4s %-42s %8s %8s %9s %8s %8s\n", "ID", "scenario", "success%", "reset", "timeout", "avg", "p95")
	for _, sc := range runs {
		st := results[sc.id]
		if st == nil {
			continue
		}
		fmt.Printf("%-4s %-42s %7.0f%% %8d %9d %8s %8s\n",
			sc.id, truncate(sc.name, 42), st.successRate(), st.reset, st.timeout,
			st.avg().Round(time.Millisecond), st.p95().Round(time.Millisecond))
	}
	fmt.Println("\nReading the table: if reset spikes only in C (aggressive) → rate/concurrency.")
	fmt.Println("If E (HTTP/1.1) or D (no keep-alive) fixes it → it's an h2 / connection-reuse issue.")
	fmt.Println("If F (session) fixes it → MIS wants an index.jsp cookie first.")
	fmt.Println("If A (baseline) already resets → upstream/proxy/network, not your client.")
}

// ── helpers ─────────────────────────────────────────────────────────────────

func splitCodes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
