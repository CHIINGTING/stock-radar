package market

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout = 10 * time.Second
	maxRetries     = 3 // up to 3 retries after the first attempt
)

// backoffSchedule is the wait before retry attempts 1, 2, 3 (1s / 2s / 4s).
var backoffSchedule = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

// yahooClient is the default client for Yahoo Finance (history + intraday) when a
// provider has no explicit client. HTTP/2 stays on (Yahoo serves h2 fine); flip
// it via NewHTTPClient if a network resets h2 to Yahoo too.
var yahooClient = buildHTTPClient(requestTimeout, false, false, 0)

// buildHTTPClient constructs a client with optional HTTP/2 disabling and
// keep-alive control. A non-nil empty TLSNextProto map disables the automatic
// HTTP/2 upgrade (the fix for TWSE MIS connection resets).
func buildHTTPClient(timeout time.Duration, forceHTTP1, disableKeepAlive bool, maxIdlePerHost int) *http.Client {
	tr := &http.Transport{
		DisableKeepAlives:   disableKeepAlive,
		ForceAttemptHTTP2:   !forceHTTP1,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: maxIdlePerHost,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
	}
	if forceHTTP1 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// NewHTTPClient builds a client for the Yahoo providers with the given options.
func NewHTTPClient(timeout time.Duration, forceHTTP1, disableKeepAlive bool) *http.Client {
	return buildHTTPClient(timeout, forceHTTP1, disableKeepAlive, 0)
}

// requestJitter returns a random 100–300ms delay applied before every request to
// spread out bursts that trigger upstream EOF / rate limiting.
func requestJitter() time.Duration {
	return time.Duration(100+rand.Intn(201)) * time.Millisecond
}

// httpGet issues a GET with per-request jitter and retries transient failures
// (EOF, timeout, connection reset, HTTP 429 / 5xx) with 1s/2s/4s backoff, up to
// maxRetries. Every attempt is recorded in GlobalMetrics. 404 and other 4xx
// statuses are returned as-is (not retried) so callers can detect "not found".
func httpGet(client *http.Client, url string, headers map[string]string) (int, []byte, error) {
	var lastErr error
	lastStatus := 0

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoffSchedule[attempt-1])
		}
		time.Sleep(requestJitter())

		start := time.Now()
		status, body, err := doOnce(client, url, headers)
		dur := time.Since(start)
		GlobalMetrics.record(err == nil && status < 400, isEOF(err), attempt > 0, dur)

		switch {
		case err == nil && !retryableStatus(status):
			return status, body, nil
		case err == nil:
			lastStatus, lastErr = status, fmt.Errorf("upstream status %d", status)
		default:
			lastStatus, lastErr = 0, err
		}
	}
	return lastStatus, nil, fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
}

func doOnce(client *http.Client, url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err // EOF mid-read → retryable
	}
	return resp.StatusCode, body, nil
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// isEOF reports whether err is (or wraps) an end-of-stream error — the dominant
// failure mode against the TWSE MIS endpoint.
func isEOF(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "EOF")
}

func yahooHeaders() map[string]string {
	return map[string]string{
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120.0 Safari/537.36",
		"Accept":     "application/json,text/plain,*/*",
		"Referer":    "https://finance.yahoo.com/",
	}
}

