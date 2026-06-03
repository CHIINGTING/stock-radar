package market

import (
	"testing"
	"time"
)

// bar1m builds a 1-minute candle at HH:MM Taiwan time on a fixed date.
func bar1m(h, m int, o, hi, lo, c float64, v int64) Candle {
	return Candle{
		Date:   time.Date(2026, 6, 3, h, m, 0, 0, twLoc),
		Open:   o,
		High:   hi,
		Low:    lo,
		Close:  c,
		Volume: v,
	}
}

func TestAggregateGroupsAndAligns(t *testing.T) {
	// 09:00–09:04 → expect one 5m bar and (3m) 09:00-09:02 + 09:03-09:04.
	bars := []Candle{
		bar1m(9, 0, 10, 11, 9, 10.5, 100),
		bar1m(9, 1, 10.5, 12, 10, 11, 100),
		bar1m(9, 2, 11, 11.5, 10.8, 11.2, 100),
		bar1m(9, 3, 11.2, 13, 11, 12.5, 100),
		bar1m(9, 4, 12.5, 12.8, 12, 12.3, 100),
	}

	got5 := aggregate(bars, 5)
	if len(got5) != 1 {
		t.Fatalf("5m: want 1 bar, got %d", len(got5))
	}
	b := got5[0]
	if b.Open != 10 || b.Close != 12.3 || b.High != 13 || b.Low != 9 || b.Volume != 500 {
		t.Errorf("5m OHLCV wrong: %+v", b)
	}

	got3 := aggregate(bars, 3)
	if len(got3) != 2 {
		t.Fatalf("3m: want 2 bars (09:00-02, 09:03-04), got %d", len(got3))
	}
	if got3[0].Open != 10 || got3[0].Close != 11.2 || got3[0].Volume != 300 {
		t.Errorf("3m bar0 wrong: %+v", got3[0])
	}
	if got3[1].Open != 11.2 || got3[1].Close != 12.3 || got3[1].Volume != 200 {
		t.Errorf("3m bar1 wrong: %+v", got3[1])
	}
}

func TestAggregate15mBoundary(t *testing.T) {
	// 09:14 and 09:15 must land in different 15m buckets (…09:00-09:14 / 09:15-…).
	bars := []Candle{
		bar1m(9, 14, 10, 10, 10, 10, 50),
		bar1m(9, 15, 20, 20, 20, 20, 50),
	}
	got := aggregate(bars, 15)
	if len(got) != 2 {
		t.Fatalf("want 2 separate 15m bars, got %d", len(got))
	}
}

func TestMergeLiveTailSameMinuteExtends(t *testing.T) {
	// Build the last bar at the current minute so the quote folds into it.
	now := time.Now().In(twLoc).Truncate(time.Minute)
	bars := []Candle{
		{Date: now.Add(-time.Minute), Open: 10, High: 10, Low: 10, Close: 10, Volume: 100},
		{Date: now, Open: 11, High: 11, Low: 11, Close: 11, Volume: 100},
	}
	q := &Quote{Price: 12, High: 12.5, Low: 10.5, Volume: 1} // 1 lot = 1000 shares
	out := mergeLiveTail(bars, q)

	if len(out) != 2 {
		t.Fatalf("same minute should not append, got %d bars", len(out))
	}
	last := out[1]
	if last.Close != 12 {
		t.Errorf("close not updated to live price: %v", last.Close)
	}
	if last.High != 12.5 {
		t.Errorf("high not extended: %v", last.High)
	}
	if last.Low != 10.5 {
		t.Errorf("low not extended: %v", last.Low)
	}
}

func TestMergeLiveTailNewMinuteAppends(t *testing.T) {
	now := time.Now().In(twLoc).Truncate(time.Minute)
	bars := []Candle{
		{Date: now.Add(-2 * time.Minute), Open: 10, High: 10, Low: 10, Close: 10, Volume: 100},
	}
	q := &Quote{Price: 12, High: 12, Low: 12, Volume: 1}
	out := mergeLiveTail(bars, q)
	if len(out) != 2 {
		t.Fatalf("new minute should append a tail bar, got %d", len(out))
	}
	if out[1].Close != 12 || !out[1].Date.Equal(now) {
		t.Errorf("tail bar wrong: %+v", out[1])
	}
}
