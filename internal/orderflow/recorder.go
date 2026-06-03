// Package orderflow tracks the recent history of bid/ask queue pressure for each
// stock so the Radar can judge whether buying interest is strengthening over the
// last few minutes, rather than reacting to a single instantaneous snapshot.
package orderflow

import (
	"sync"
	"time"
)

// DefaultWindow is how far back order-flow snapshots are retained.
const DefaultWindow = 15 * time.Minute

// Snapshot is one observation of the top-of-book queue at a point in time.
type Snapshot struct {
	T      time.Time `json:"t"`
	BidVol int64     `json:"bid_vol"`
	AskVol int64     `json:"ask_vol"`
	Ratio  float64   `json:"ratio"` // BidVol / AskVol (0 when AskVol == 0)
}

// Recorder keeps a rolling, time-bounded series of order-flow snapshots per code.
// It is safe for concurrent use; the cache refresh loop feeds it on every poll.
type Recorder struct {
	mu     sync.Mutex
	window time.Duration
	data   map[string][]Snapshot
}

// NewRecorder returns a Recorder retaining DefaultWindow of history.
func NewRecorder() *Recorder {
	return &Recorder{
		window: DefaultWindow,
		data:   make(map[string][]Snapshot),
	}
}

// Record appends a snapshot for code at time t and drops anything older than the
// retention window. Consecutive identical readings are still recorded so the
// trend regression sees the true sampling cadence.
func (r *Recorder) Record(code string, bidVol, askVol int64, t time.Time) {
	var ratio float64
	if askVol > 0 {
		ratio = float64(bidVol) / float64(askVol)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	series := append(r.data[code], Snapshot{T: t, BidVol: bidVol, AskVol: askVol, Ratio: ratio})
	r.data[code] = trim(series, t.Add(-r.window))
}

// Series returns a copy of the retained snapshots for code, oldest first.
func (r *Recorder) Series(code string) []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	src := r.data[code]
	if len(src) == 0 {
		return nil
	}
	out := make([]Snapshot, len(src))
	copy(out, src)
	return out
}

// trim drops snapshots strictly older than cutoff. Input is chronological.
func trim(s []Snapshot, cutoff time.Time) []Snapshot {
	i := 0
	for i < len(s) && s[i].T.Before(cutoff) {
		i++
	}
	if i == 0 {
		return s
	}
	// Compact in place to avoid retaining the trimmed prefix.
	return append(s[:0], s[i:]...)
}
