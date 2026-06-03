package radar

import (
	"testing"
	"time"

	"stock-radar/internal/market"
	"stock-radar/internal/orderflow"
)

// trendingBars builds n bars whose closes rise (up) or fall, so MA5>MA20 (or <)
// deterministically. The last bar carries lastVol; the rest baseVol.
func trendingBars(n int, up bool, baseVol, lastVol int64) []market.Candle {
	bars := make([]market.Candle, n)
	base := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		var price float64
		if up {
			price = 100 + float64(i)
		} else {
			price = 100 - float64(i)
		}
		v := baseVol
		if i == n-1 {
			v = lastVol
		}
		bars[i] = market.Candle{
			Date:   base.Add(time.Duration(i) * time.Minute),
			Open:   price, High: price + 0.5, Low: price - 0.5, Close: price, Volume: v,
		}
	}
	return bars
}

func breakoutBars(n int, baseVol, lastVol int64) []market.Candle {
	bars := trendingBars(n, true, baseVol, lastVol)
	last := &bars[n-1]
	last.Close = bars[n-2].High + 5
	last.High = last.Close + 0.5
	return bars
}

// pullbackBars: a clean uptrend that dips toward MA20 then reclaims and turns up.
func pullbackBars(n int) []market.Candle {
	bars := trendingBars(n, true, 1000, 1000)
	ma := 0.0
	for _, b := range bars[n-20:] {
		ma += b.Close
	}
	ma /= 20
	// Carve a dip in the bars just before the last, touching MA, then reclaim.
	bars[n-3].Low = ma * 1.00
	bars[n-3].Close = ma * 1.005
	bars[n-2].Low = ma * 0.995
	bars[n-2].Close = ma * 1.002
	// Last bar reclaims above MA and turns up.
	bars[n-1].Close = ma * 1.02
	bars[n-1].High = ma * 1.03
	bars[n-1].Low = ma * 1.005
	return bars
}

func flowOf(trend string, ratio, slope float64) orderflow.Flow {
	return orderflow.Flow{Trend: trend, Ratio: ratio, RatioSlope: slope, Samples: 5}
}

func cfg() Config { return Config{BuyScore: 80, WatchScore: 60} }

func TestStrongBuyAligned(t *testing.T) {
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 2500),
		Bars5m:  trendingBars(25, true, 1000, 2500),
		Bars3m:  breakoutBars(25, 1000, 3000),
	}
	sig := Analyze(&market.Quote{Code: "2337", Name: "旺宏", Price: 173}, set,
		flowOf(orderflow.TrendStrengthening, 3.1, 0.2), cfg())

	if sig.Action != ActionStrongBuy {
		t.Errorf("want STRONG BUY, got %s (score=%d)", sig.Action, sig.Score)
	}
	if !sig.CanBuy || sig.Momentum != MomStrong {
		t.Errorf("expected CanBuy + STRONG momentum, got canBuy=%v mom=%s", sig.CanBuy, sig.Momentum)
	}
}

func TestPullbackBuy(t *testing.T) {
	// Uptrend on 15m, healthy pullback reclaimed on 5m, flow not weakening.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 1500),
		Bars5m:  pullbackBars(25),
		Bars3m:  trendingBars(25, true, 1000, 1200),
	}
	sig := Analyze(&market.Quote{Code: "5483", Name: "中美晶", Price: 165}, set,
		flowOf(orderflow.TrendFlat, 1.2, 0.0), cfg())

	if !sig.PullbackBuy {
		t.Fatalf("pullback should be detected")
	}
	if sig.Action != ActionPullbackBuy {
		t.Errorf("want PULLBACK BUY, got %s", sig.Action)
	}
	if !sig.CanBuy {
		t.Errorf("pullback buy should be buyable")
	}
}

// fadingBars3m: uptrend with the last three 3m bars on declining, below-average
// volume → triggers Volume Fade.
func fadingBars3m() []market.Candle {
	bars := trendingBars(25, true, 1000, 1000)
	bars[22].Volume = 600
	bars[23].Volume = 400
	bars[24].Volume = 200
	return bars
}

func TestExitLevel1VolumeFadeAlert(t *testing.T) {
	// Volume fade alone, trends still up → TAKE PROFIT ALERT (not exit).
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 1000),
		Bars5m:  trendingBars(25, true, 1000, 1000),
		Bars3m:  fadingBars3m(),
	}
	sig := Analyze(&market.Quote{Code: "x", Price: 50}, set,
		flowOf(orderflow.TrendFlat, 1.3, 0.0), cfg())

	if sig.ExitLevel != 1 {
		t.Fatalf("want exit level 1, got %d (volFade=%v)", sig.ExitLevel, sig.VolumeFade)
	}
	if sig.Action != ActionTakeProfitAlert {
		t.Errorf("want TAKE PROFIT ALERT, got %s", sig.Action)
	}
	if sig.ShouldExit {
		t.Errorf("level 1 must not be should-exit")
	}
}

func TestExitLevel2TakeProfit(t *testing.T) {
	// Volume fade + buy flow weakening → TAKE PROFIT (Level 2).
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 1000),
		Bars5m:  trendingBars(25, true, 1000, 1000),
		Bars3m:  fadingBars3m(),
	}
	sig := Analyze(&market.Quote{Code: "x", Price: 50}, set,
		flowOf(orderflow.TrendWeakening, 0.8, -0.3), cfg())

	if sig.ExitLevel != 2 {
		t.Fatalf("want exit level 2, got %d", sig.ExitLevel)
	}
	if sig.Action != ActionTakeProfit {
		t.Errorf("want TAKE PROFIT, got %s", sig.Action)
	}
	if sig.ShouldExit {
		t.Errorf("level 2 is profit-taking, not should-exit")
	}
}

func TestExitLevel3Exit(t *testing.T) {
	// Fade + flow weak + 5m break support → EXIT (Level 3).
	// 5m falling so MA5<MA20 and last close below MA20 = break support.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, true, 1000, 1000), // 15m still up
		Bars5m:  trendingBars(25, false, 1000, 1000),
		Bars3m:  fadingBars3m(),
	}
	sig := Analyze(&market.Quote{Code: "x", Price: 50}, set,
		flowOf(orderflow.TrendWeakening, 0.8, -0.3), cfg())

	if !sig.Break5mSupport {
		t.Fatalf("expected 5m break support")
	}
	if sig.ExitLevel != 3 || sig.Action != ActionExit {
		t.Errorf("want Level 3 EXIT, got level=%d action=%s", sig.ExitLevel, sig.Action)
	}
	if !sig.ShouldExit {
		t.Errorf("level 3 should be should-exit")
	}
}

func TestExitLevel4ForceExit(t *testing.T) {
	// 15m bearish → FORCE EXIT regardless of volume.
	set := market.IntradaySet{
		Bars15m: trendingBars(25, false, 1000, 3000), // bearish, but high volume
		Bars5m:  trendingBars(25, false, 1000, 3000),
		Bars3m:  trendingBars(25, false, 1000, 3000),
	}
	sig := Analyze(&market.Quote{Code: "x", Price: 50}, set,
		flowOf(orderflow.TrendStrengthening, 2.0, 0.2), cfg())

	if sig.ExitLevel != 4 || sig.Action != ActionForceExit {
		t.Errorf("want Level 4 FORCE EXIT, got level=%d action=%s", sig.ExitLevel, sig.Action)
	}
	if sig.Momentum != MomDead {
		t.Errorf("want DEAD, got %s", sig.Momentum)
	}
}

func TestVolumeFadeDetection(t *testing.T) {
	// Declining 3m volume below average = fade.
	bars := trendingBars(25, true, 1000, 1000)
	bars[24].Volume = 200
	bars[23].Volume = 400
	bars[22].Volume = 600
	if !detectVolumeFade(bars, volRatio(bars), 0.5) {
		t.Errorf("declining low volume should be a fade")
	}
	// Rising volume = no fade.
	rising := trendingBars(25, true, 1000, 3000)
	if detectVolumeFade(rising, volRatio(rising), 2.0) {
		t.Errorf("rising volume should not be a fade")
	}
}

func TestNo3mSpikeAloneBuy(t *testing.T) {
	// 3m breakout but 15m & 5m bearish → never BUY (and likely EXIT).
	set := market.IntradaySet{
		Bars15m: trendingBars(25, false, 1000, 1000),
		Bars5m:  trendingBars(25, false, 1000, 1000),
		Bars3m:  breakoutBars(25, 1000, 4000),
	}
	sig := Analyze(&market.Quote{Code: "9999", Price: 50}, set,
		flowOf(orderflow.TrendWeakening, 0.6, -0.3), cfg())
	if sig.CanBuy {
		t.Errorf("bearish trends must not be buyable, got %s", sig.Action)
	}
}

func TestMainForceClassification(t *testing.T) {
	if got := classifyMainForce(3.5, 2.8, 2.5); got != "主流股" {
		t.Errorf("consistent expansion → 主流股, got %q", got)
	}
	if got := classifyMainForce(3.5, 1.1, 0.8); got != "短線炒作" {
		t.Errorf("3m-only spike → 短線炒作, got %q", got)
	}
}
