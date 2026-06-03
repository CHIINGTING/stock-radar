// Package radar provides intraday (day-trading) multi-timeframe analysis.
//
// It answers four questions a day trader actually asks:
//
//	今天能不能做？  — overall Score / Action
//	方向對不對？    — 15m trend (the gate)
//	是不是突破點？  — 3m breakout
//	主力進場沒？    — order-flow trend + multi-timeframe volume
//
// Three timeframes carry distinct roles: 15m decides direction, 5m finds the
// entry, 3m confirms ignition. A BUY is only allowed when all three align.
package radar

import (
	"fmt"
	"math"

	"stock-radar/internal/market"
	"stock-radar/internal/orderflow"
	"stock-radar/internal/strategy"
)

// Trend labels for a single timeframe.
const (
	Bullish  = "Bullish"
	Bearish  = "Bearish"
	Neutral  = "Neutral"
	Breakout = "Breakout" // 3m only: volume spike + breaking recent high
)

// Action labels, ordered from most bullish to most bearish.
const (
	ActionStrongBuy = "STRONG BUY"
	ActionBuy       = "BUY"
	ActionWatch     = "WATCH"
	ActionWait      = "WAIT"
	ActionReduce    = "REDUCE"
	ActionStopLoss  = "STOP LOSS"
)

// Scoring weights (sum = 100). These are the day-trading weights: direction and
// order flow dominate; the 3m breakout is a confirming trigger, not a driver.
const (
	wTrend15m   = 30.0 // 15分趨勢
	wVolTrend   = 25.0 // 單量趨勢（多時間框量能）
	wBidAsk     = 20.0 // 買賣比
	wTrend5m    = 15.0 // 5分趨勢
	wBreakout3m = 10.0 // 3分突破
)

// breakoutVolRatio is the 3m volume multiple required to call ignition.
const breakoutVolRatio = 2.0

// Config carries the BUY / WATCH score thresholds (from stocks.yaml).
type Config struct {
	BuyScore   int
	WatchScore int
}

// RadarSignal is the full intraday verdict for one stock.
type RadarSignal struct {
	Code  string  `json:"code"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`

	Trend15m string `json:"trend_15m"`
	Trend5m  string `json:"trend_5m"`
	Trend3m  string `json:"trend_3m"`

	Volume15m float64 `json:"volume_15m"`
	Volume5m  float64 `json:"volume_5m"`
	Volume3m  float64 `json:"volume_3m"`

	Flow orderflow.Flow `json:"flow"`

	Score     int    `json:"score"`
	Action    string `json:"action"`
	CanTrade  bool   `json:"can_trade"`  // true when the 15m∧5m∧3m gate is open
	MainForce string `json:"main_force"` // 主流股 / 短線炒作 / ""
	Closed    bool   `json:"closed"`     // intraday data is Yahoo-only (market shut)

	Reasons []string `json:"reasons"`
}

// Analyze produces the intraday RadarSignal from aggregated bars and order flow.
func Analyze(q *market.Quote, set market.IntradaySet, flow orderflow.Flow, cfg Config) RadarSignal {
	t15 := trendOf(set.Bars15m)
	t5 := trendOf(set.Bars5m)

	vol3 := volRatio(set.Bars3m)
	vol5 := volRatio(set.Bars5m)
	vol15 := volRatio(set.Bars15m)

	t3 := trend3m(set.Bars3m, vol3)

	var reasons []string

	// ── Component scores, each normalised to 0..1 ──────────────
	c15 := trendComponent(t15)
	c5 := trendComponent(t5)
	cVol := volTrendComponent(vol3, vol5, vol15)
	cFlow := flowComponent(flow)
	cBreak := breakoutComponent(t3)

	score := int(math.Round(
		wTrend15m*c15 +
			wTrend5m*c5 +
			wVolTrend*cVol +
			wBidAsk*cFlow +
			wBreakout3m*cBreak,
	))

	// ── Reasons (plain-language, not raw indicator dumps) ──────
	reasons = append(reasons, fmt.Sprintf("15分%s（方向）", zhTrend(t15)))
	reasons = append(reasons, fmt.Sprintf("5分%s（進場）", zhTrend(t5)))
	reasons = append(reasons, fmt.Sprintf("3分%s（點火）", zhTrend(t3)))
	if flow.Trend != "" {
		reasons = append(reasons, fmt.Sprintf("買盤%s（買賣比 %.2f）", flow.Trend, flow.Ratio))
	}

	mainForce := classifyMainForce(vol3, vol5, vol15)
	if mainForce != "" {
		reasons = append(reasons, fmt.Sprintf("%s（3分%.1fx／5分%.1fx／15分%.1fx）", mainForce, vol3, vol5, vol15))
	}

	gate := t15 == Bullish && t5 == Bullish && t3 == Breakout
	action := decideAction(gate, t15, t5, t3, score, flow, cfg)

	return RadarSignal{
		Code:      q.Code,
		Name:      q.Name,
		Price:     q.Price,
		Trend15m:  t15,
		Trend5m:   t5,
		Trend3m:   t3,
		Volume15m: vol15,
		Volume5m:  vol5,
		Volume3m:  vol3,
		Flow:      flow,
		Score:     score,
		Action:    action,
		CanTrade:  gate,
		MainForce: mainForce,
		Closed:    set.Closed,
		Reasons:   reasons,
	}
}

// decideAction applies the day-trading action ladder. The cardinal rule: a BUY
// requires the full 15m∧5m∧3m gate — a 3m volume spike alone never qualifies.
func decideAction(gate bool, t15, t5, t3 string, score int, flow orderflow.Flow, cfg Config) string {
	buyScore := cfg.BuyScore
	watchScore := cfg.WatchScore
	if buyScore == 0 {
		buyScore = 80
	}
	if watchScore == 0 {
		watchScore = 60
	}

	hasShortSignal := t3 == Breakout || t5 == Bullish || t3 == Bullish

	switch {
	case gate && score >= buyScore+7 && flow.Trend == orderflow.TrendStrengthening:
		return ActionStrongBuy
	case gate && score >= buyScore:
		return ActionBuy
	case gate:
		// Aligned but not strong enough to commit — keep watching.
		return ActionWatch
	case t15 == Bearish && t5 == Bearish && flow.Trend == orderflow.TrendWeakening:
		return ActionStopLoss
	case t15 == Bearish && !hasShortSignal:
		return ActionReduce
	case hasShortSignal && score >= watchScore-10:
		// Shorter timeframes firing while direction not yet confirmed.
		return ActionWatch
	default:
		return ActionWait
	}
}

// trendOf classifies a timeframe by MA5 vs MA20 (falling back to price-vs-MA5
// when there is not yet enough history for MA20).
func trendOf(c []market.Candle) string {
	if len(c) < 3 {
		return Neutral
	}
	ma5 := strategy.MA(c, 5)
	ma20 := strategy.MA(c, 20)

	if ma20 == 0 || ma5 == 0 {
		// Early session: compare last close to whatever short MA we have.
		ref := ma5
		if ref == 0 {
			ref = c[0].Close
		}
		last := c[len(c)-1].Close
		switch {
		case last > ref*1.001:
			return Bullish
		case last < ref*0.999:
			return Bearish
		default:
			return Neutral
		}
	}

	switch {
	case ma5 > ma20*1.0005:
		return Bullish
	case ma5 < ma20*0.9995:
		return Bearish
	default:
		return Neutral
	}
}

// trend3m returns Breakout when the 3m frame shows ignition (volume spike while
// breaking the recent high), otherwise the ordinary MA-based trend.
func trend3m(c []market.Candle, vol3 float64) string {
	if isBreakout(c, vol3) {
		return Breakout
	}
	return trendOf(c)
}

// isBreakout reports a volume-confirmed break above the prior-10-bar high.
func isBreakout(c []market.Candle, volR float64) bool {
	n := len(c)
	if n < 6 {
		return false
	}
	look := 10
	if n-1 < look {
		look = n - 1
	}
	hi := 0.0
	for _, b := range c[n-1-look : n-1] {
		if b.High > hi {
			hi = b.High
		}
	}
	last := c[n-1]
	return volR >= breakoutVolRatio && hi > 0 && last.Close > hi
}

// volRatio returns the latest bar's volume relative to the trailing average
// (up to 20 prior bars). Returns 0 when there is not enough history.
func volRatio(c []market.Candle) float64 {
	n := len(c)
	if n < 2 {
		return 0
	}
	look := 20
	if n-1 < look {
		look = n - 1
	}
	var sum int64
	for _, b := range c[n-1-look : n-1] {
		sum += b.Volume
	}
	if look == 0 {
		return 0
	}
	avg := float64(sum) / float64(look)
	if avg <= 0 {
		return 0
	}
	return r2(float64(c[n-1].Volume) / avg)
}

// classifyMainForce distinguishes broad institutional participation from a thin
// short-term ramp, using volume consistency across timeframes.
func classifyMainForce(vol3, vol5, vol15 float64) string {
	switch {
	case vol3 >= 1.5 && vol5 >= 1.5 && vol15 >= 1.5:
		return "主流股"
	case vol3 >= 2.0 && vol15 > 0 && vol15 < 1.0:
		return "短線炒作"
	default:
		return ""
	}
}

// ── Component normalisers (0..1) ───────────────────────────────

func trendComponent(t string) float64 {
	switch t {
	case Bullish, Breakout:
		return 1.0
	case Neutral:
		return 0.5
	default:
		return 0.0
	}
}

// volTrendComponent scores multi-timeframe volume expansion. Each timeframe maps
// 0.8x→0 .. 2.0x→1, then averages — so consistent expansion (主流股) scores high.
func volTrendComponent(vol3, vol5, vol15 float64) float64 {
	return (volScale(vol3) + volScale(vol5) + volScale(vol15)) / 3
}

func volScale(r float64) float64 {
	return clamp01((r - 0.8) / (2.0 - 0.8))
}

// flowComponent blends the order-flow direction (60%) with the absolute ratio
// level (40%).
func flowComponent(f orderflow.Flow) float64 {
	var dir float64
	switch f.Trend {
	case orderflow.TrendStrengthening:
		dir = 1.0
	case orderflow.TrendWeakening:
		dir = 0.0
	default:
		dir = 0.5
	}
	level := clamp01((f.Ratio - 0.8) / (2.0 - 0.8))
	return 0.6*dir + 0.4*level
}

func breakoutComponent(t3 string) float64 {
	switch t3 {
	case Breakout:
		return 1.0
	case Bullish:
		return 0.5
	case Neutral:
		return 0.3
	default:
		return 0.0
	}
}

func zhTrend(t string) string {
	switch t {
	case Bullish:
		return "偏多"
	case Bearish:
		return "偏空"
	case Breakout:
		return "突破"
	default:
		return "中性"
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func r2(v float64) float64 {
	return math.Round(v*100) / 100
}
