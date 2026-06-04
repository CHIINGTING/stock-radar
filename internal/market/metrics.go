package market

import (
	"sync"
	"time"
)

// Metrics tracks outbound data-fetch health so EOF / rate-limit problems can be
// diagnosed without changing the data source. All outbound requests (MIS + Yahoo)
// funnel through httpGet, which records into GlobalMetrics.
type Metrics struct {
	mu        sync.Mutex
	requests  int64
	successes int64
	eofs      int64
	retries   int64
	totalMs   int64
}

// GlobalMetrics is the process-wide fetch metrics sink.
var GlobalMetrics = &Metrics{}

// record logs one attempt. isRetry is true for attempts after the first.
func (m *Metrics) record(success, eof, isRetry bool, dur time.Duration) {
	m.mu.Lock()
	m.requests++
	if success {
		m.successes++
	}
	if eof {
		m.eofs++
	}
	if isRetry {
		m.retries++
	}
	m.totalMs += dur.Milliseconds()
	m.mu.Unlock()
}

// MetricsSnapshot is a point-in-time view of the fetch metrics.
type MetricsSnapshot struct {
	Requests    int64   `json:"requests"`
	Successes   int64   `json:"successes"`
	SuccessRate float64 `json:"success_rate"`     // 0..1
	EOFCount    int64   `json:"eof_count"`
	Retries     int64   `json:"retries"`
	AvgMs       float64 `json:"avg_response_ms"`
}

// Snapshot returns the current aggregated metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := MetricsSnapshot{
		Requests:  m.requests,
		Successes: m.successes,
		EOFCount:  m.eofs,
		Retries:   m.retries,
	}
	if m.requests > 0 {
		s.SuccessRate = float64(m.successes) / float64(m.requests)
		s.AvgMs = float64(m.totalMs) / float64(m.requests)
	}
	return s
}
