package market

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const validMIS = `{"msgArray":[{"c":"2330","n":"台積電","z":"1000.0","y":"990.0","o":"995","h":"1005","l":"992","v":"12345","b":"999.0_998.0_","a":"1001.0_1002.0_","g":"10_20_","f":"5_8_"}]}`

// newTestClient returns a TwseClient pointed at srv with fast retries and a hint
// so it queries the tse market directly.
func newTestClient(srv *httptest.Server) *TwseClient {
	cfg := DefaultTwseConfig()
	cfg.Backoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	cfg.JitterMax = time.Millisecond
	c := NewTwseClient(cfg, map[string]string{"2330": "tse"})
	c.http = srv.Client()
	c.baseURL = srv.URL
	return c
}

func TestTwseRetriesEOFThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			// Hijack + close → EOF-class transient error.
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		w.Write([]byte(validMIS))
	}))
	defer srv.Close()

	q, err := newTestClient(srv).GetQuote("2330")
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if q.Code != "2330" || q.Price != 1000 {
		t.Errorf("bad quote: %+v", q)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("want 3 attempts (2 retries), got %d", got)
	}
}

func TestTwseKeepsLastGoodOnFailure(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		w.Write([]byte(validMIS))
	}))
	defer srv.Close()

	c := newTestClient(srv)

	// First call succeeds and populates last-good.
	q1, err := c.GetQuote("2330")
	if err != nil || q1.Price != 1000 {
		t.Fatalf("first call failed: q=%v err=%v", q1, err)
	}

	// Now make the server fail every time; GetQuote must serve the stale quote.
	fail.Store(true)
	q2, err := c.GetQuote("2330")
	if err != nil {
		t.Fatalf("expected stale-cache success, got error %v", err)
	}
	if q2.Price != 1000 {
		t.Errorf("expected last-good quote, got %+v", q2)
	}
}

func TestTwseFirstFailureReturnsError(t *testing.T) {
	// No prior success → a failure must surface as an error (no cache to fall back to).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()

	if _, err := newTestClient(srv).GetQuote("2330"); err == nil {
		t.Fatal("expected error on first-ever failure with no cache")
	}
}

func TestTwseSingleFlightCollapsesConcurrent(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(80 * time.Millisecond) // hold so concurrent callers overlap
		w.Write([]byte(validMIS))
	}))
	defer srv.Close()

	c := newTestClient(srv)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.GetQuote("2330"); err != nil {
				t.Errorf("concurrent GetQuote err: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("single-flight should collapse 8 concurrent calls into 1, got %d server hits", got)
	}
}

func TestTwseNotFoundFallsToOTC(t *testing.T) {
	// tse → empty msgArray (not found); otc → valid. Auto-detect should reach otc.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ex := r.URL.Query().Get("ex_ch"); ex == "tse_5483.tw" {
			w.Write([]byte(`{"msgArray":[]}`))
			return
		}
		w.Write([]byte(validMIS))
	}))
	defer srv.Close()

	cfg := DefaultTwseConfig()
	cfg.Backoffs = []time.Duration{time.Millisecond}
	c := NewTwseClient(cfg, nil) // no hint → auto-detect
	c.http = srv.Client()
	c.baseURL = srv.URL

	q, err := c.GetQuote("5483")
	if err != nil {
		t.Fatalf("auto-detect to OTC failed: %v", err)
	}
	if q.Code != "2330" { // server returns the canned valid body for otc
		t.Errorf("unexpected quote: %+v", q)
	}
}
