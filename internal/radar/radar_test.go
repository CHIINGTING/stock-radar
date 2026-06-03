package radar

import (
	"testing"
	"time"

	"stock-radar/internal/market"
	"stock-radar/internal/orderflow"
)

// trendingBars builds n bars whose closes rise (up=true) or fall, so MA5>MA20
// (or <) deterministically. The last bar carries lastVol; the rest carry baseVol
// so volRatio(last) ≈ lastVol/baseVol.
func trendingBars(n int, up bool, baseVol, lastVol int64) []market.Candle {
	bars := make([]market.Candle, n)
	base := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		var price float64
		if up {
			price = 100 + float64(i) // rising → MA5 > MA20
		} else {
			price = 100 - float64(i) // falling → MA5 < MA20
		}
		v := baseVol
		if i == n-1 {
			v = lastVol
		}
		bars[i] = market.Candle{
			Date:   base.Add(time.Duration(i) * time.Minute),
			Open:   price,
			High:   price + 0.5,
			Low:    price - 0.5,
			Close:  price,
			Volume: v,
		}
	}
	return bars
}

// breakoutBars makes the last close exceed the prior-bar highs on a volume spike.
func breakoutBars(n int, baseVol, lastVol int64) []market.Candle {
	bars := trendingBars(n, true, baseVol, lastVol)
	last := &bars[n-1]
	last.Close = bars[n-2].High + 5 // clear break above recent high
	last.High = last.Close + 0.5
	return bars
}

func flowOf(trend string, ratio float64) orderflow.Flow {
	return orderflow.Flow{Trend: trend, Ratio: ratio, Samples: 5}
}

func TestStrongBuyAligned(t *testing.T) {
	// 15m bullish, 5m bullish, 3m breakout, strong volume + strengthening flow.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 2500),
		Bars5m:  trendingBars(25, true, 1000, 2500),
		Bars3m:  breakoutBars(25, 1000, 3000),
	}
	sig := Analyze(
		&market.Quote{Code: "2337", Name: "旺宏", Price: 173},
		set,
		flowOf(orderflow.TrendStrengthening, 3.1),
		Config{BuyScore: 80, WatchScore: 60},
	)

	if sig.Trend15m != Bullish || sig.Trend5m != Bullish || sig.Trend3m != Breakout {
		t.Fatalf("trends: 15=%s 5=%s 3=%s", sig.Trend15m, sig.Trend5m, sig.Trend3m)
	}
	if !sig.CanTrade {
		t.Errorf("gate should be open")
	}
	if sig.Action != ActionStrongBuy {
		t.Errorf("want STRONG BUY, got %s (score=%d)", sig.Action, sig.Score)
	}
	if sig.Score < 85 {
		t.Errorf("aligned strong setup should score high, got %d", sig.Score)
	}
}

func TestWatchWhen15mBearish(t *testing.T) {
	// 15m bearish but 5m bullish + 3m breakout → not BUY, should WATCH.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, false, 1000, 1200),
		Bars5m:  trendingBars(25, true, 1000, 2000),
		Bars3m:  breakoutBars(25, 1000, 2500),
	}
	sig := Analyze(
		&market.Quote{Code: "2337", Name: "旺宏", Price: 170},
		set,
		flowOf(orderflow.TrendFlat, 1.4),
		Config{BuyScore: 80, WatchScore: 60},
	)

	if sig.Trend15m != Bearish || sig.Trend5m != Bullish || sig.Trend3m != Breakout {
		t.Fatalf("trends: 15=%s 5=%s 3=%s", sig.Trend15m, sig.Trend5m, sig.Trend3m)
	}
	if sig.CanTrade {
		t.Errorf("gate must be closed when 15m bearish")
	}
	if sig.Action != ActionWatch {
		t.Errorf("want WATCH, got %s (score=%d)", sig.Action, sig.Score)
	}
}

func TestNo3mSpikeAloneBuy(t *testing.T) {
	// 3m breakout alone (15m & 5m bearish) must never BUY.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, false, 1000, 1000),
		Bars5m:  trendingBars(25, false, 1000, 1000),
		Bars3m:  breakoutBars(25, 1000, 4000),
	}
	sig := Analyze(
		&market.Quote{Code: "9999", Price: 50},
		set,
		flowOf(orderflow.TrendWeakening, 0.6),
		Config{BuyScore: 80, WatchScore: 60},
	)
	if sig.Action == ActionBuy || sig.Action == ActionStrongBuy {
		t.Errorf("3m spike alone must not BUY, got %s", sig.Action)
	}
}

func TestMainForceClassification(t *testing.T) {
	if got := classifyMainForce(3.5, 2.8, 2.5); got != "主流股" {
		t.Errorf("consistent expansion → 主流股, got %q", got)
	}
	if got := classifyMainForce(3.5, 1.1, 0.8); got != "短線炒作" {
		t.Errorf("3m-only spike → 短線炒作, got %q", got)
	}
	if got := classifyMainForce(1.0, 1.0, 1.0); got != "" {
		t.Errorf("flat volume → no label, got %q", got)
	}
}
