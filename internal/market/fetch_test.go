package market

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// shortBackoff swaps the retry schedule for near-instant waits during tests.
func shortBackoff(t *testing.T) {
	t.Helper()
	old := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { backoffSchedule = old })
}

func TestHttpGetRetries429ThenSucceeds(t *testing.T) {
	shortBackoff(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests) // retryable
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	status, body, err := httpGet(srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Errorf("status=%d body=%s", status, body)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("want 3 attempts (2 retries), got %d", got)
	}
}

func TestHttpGet404NotRetried(t *testing.T) {
	shortBackoff(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	status, _, err := httpGet(srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("404 should be returned, not errored: %v", err)
	}
	if status != http.StatusNotFound {
		t.Errorf("status=%d", status)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("404 must not retry, got %d calls", got)
	}
}

func TestHttpGetEOFRetriesAndCountsMetric(t *testing.T) {
	shortBackoff(t)
	before := GlobalMetrics.Snapshot()

	// Hijack and close the connection → client sees an EOF-class error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()

	if _, _, err := httpGet(srv.Client(), srv.URL, nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	after := GlobalMetrics.Snapshot()
	if d := after.Requests - before.Requests; d != 4 {
		t.Errorf("want 4 attempts (1 + 3 retries), got %d", d)
	}
	if d := after.EOFCount - before.EOFCount; d < 1 {
		t.Errorf("expected EOF metric to increment, delta=%d", d)
	}
	if d := after.Retries - before.Retries; d != 3 {
		t.Errorf("want 3 retry attempts recorded, got %d", d)
	}
}

func TestHttpGetSuccessRecordsMetric(t *testing.T) {
	before := GlobalMetrics.Snapshot()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if _, _, err := httpGet(srv.Client(), srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	after := GlobalMetrics.Snapshot()
	if after.Successes-before.Successes != 1 {
		t.Errorf("success not recorded")
	}
}
