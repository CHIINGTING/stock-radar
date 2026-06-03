package orderflow

import (
	"testing"
	"time"
)

func snapsFromRatios(start time.Time, ratios []float64) []Snapshot {
	out := make([]Snapshot, len(ratios))
	for i, r := range ratios {
		out[i] = Snapshot{
			T:      start.Add(time.Duration(i) * 5 * time.Minute / 4), // ~every 75s
			BidVol: int64(r * 100),
			AskVol: 100,
			Ratio:  r,
		}
	}
	return out
}

func TestAnalyzeStrengthening(t *testing.T) {
	// 1.2 → 1.8 → 2.4 → 3.1 over the window = clearly rising.
	f := Analyze(snapsFromRatios(time.Now(), []float64{1.2, 1.8, 2.4, 3.1}))
	if f.Trend != TrendStrengthening {
		t.Errorf("want 增強, got %q (slope=%.3f)", f.Trend, f.RatioSlope)
	}
	if f.Ratio != 3.1 {
		t.Errorf("latest ratio: want 3.1, got %v", f.Ratio)
	}
}

func TestAnalyzeWeakening(t *testing.T) {
	f := Analyze(snapsFromRatios(time.Now(), []float64{3.0, 2.2, 1.5, 0.8}))
	if f.Trend != TrendWeakening {
		t.Errorf("want 衰退, got %q (slope=%.3f)", f.Trend, f.RatioSlope)
	}
}

func TestAnalyzeFlat(t *testing.T) {
	f := Analyze(snapsFromRatios(time.Now(), []float64{1.5, 1.52, 1.48, 1.51}))
	if f.Trend != TrendFlat {
		t.Errorf("want 持平, got %q (slope=%.3f)", f.Trend, f.RatioSlope)
	}
}

func TestAnalyzeColdStartFlat(t *testing.T) {
	// Fewer than minSamples → flat regardless of values.
	f := Analyze(snapsFromRatios(time.Now(), []float64{1.0, 5.0}))
	if f.Trend != TrendFlat {
		t.Errorf("cold start should be 持平, got %q", f.Trend)
	}
}

func TestRecorderTrimsWindow(t *testing.T) {
	r := NewRecorder()
	base := time.Now()
	// One stale sample (20 min ago) + two fresh ones.
	r.Record("2337", 100, 100, base.Add(-20*time.Minute))
	r.Record("2337", 200, 100, base.Add(-1*time.Minute))
	r.Record("2337", 300, 100, base)

	s := r.Series("2337")
	if len(s) != 2 {
		t.Fatalf("stale sample should be trimmed, got %d", len(s))
	}
	if s[0].Ratio != 2.0 || s[1].Ratio != 3.0 {
		t.Errorf("unexpected retained ratios: %+v", s)
	}
}

func TestRecorderRatioZeroAsk(t *testing.T) {
	r := NewRecorder()
	r.Record("x", 500, 0, time.Now())
	s := r.Series("x")
	if s[0].Ratio != 0 {
		t.Errorf("zero ask should yield ratio 0, got %v", s[0].Ratio)
	}
}
