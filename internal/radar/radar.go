// Package radar provides intraday momentum analysis for day trading.
//
// The model is built around momentum, not a fixed stop price. It targets strong
// stocks and treats shrinking volume as a profit-taking signal, not a stop-loss:
//
//	有量進  →  量縮觀察  →  量縮 + 買盤退 = 出  →  趨勢翻空 = 強制出
//
// Exit is graduated across four levels rather than a single binary stop:
//
//	Level 1  Volume Fade                              → TAKE PROFIT ALERT
//	Level 2  Volume Fade + Buy Flow Weakening         → TAKE PROFIT
//	Level 3  Level 2 + 5m Break Support               → EXIT
//	Level 4  15m Trend Bearish                        → FORCE EXIT
//
// Radar answers four questions:
//
//	可以買嗎？        — CanBuy / Action
//	現在是回測買點嗎？ — PullbackBuy（Pullback Detection）
//	動能是否衰退？     — MomentumFading（Volume Fade + Buy Flow Weakening）
//	是否應立即離場？   — ShouldExit（Level 3+）
//
// Three timeframes carry distinct roles: 15m decides direction (the strong-stock
// gate), 5m finds the entry / pullback / support, 3m confirms ignition.
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

// Momentum state labels.
const (
	MomStrong  = "STRONG"  // trend + flow + volume all aligned and rising
	MomRising  = "RISING"  // trend up, no fade yet
	MomFading  = "FADING"  // fade signal active (warning)
	MomDead    = "DEAD"    // exit / force-exit triggered
	MomNeutral = "NEUTRAL" // no clear momentum
)

// Action labels. The exit side is graduated: shrinking volume is a profit-taking
// alert, only a real breakdown / trend reversal forces you out.
const (
	ActionStrongBuy       = "STRONG BUY"
	ActionBuy             = "BUY"
	ActionPullbackBuy     = "PULLBACK BUY"      // 回測買點
	ActionHold            = "HOLD"
	ActionTakeProfitAlert = "TAKE PROFIT ALERT" // Level 1
	ActionTakeProfit      = "TAKE PROFIT"       // Level 2
	ActionExit            = "EXIT"              // Level 3
	ActionForceExit       = "FORCE EXIT"        // Level 4
	ActionWait            = "WAIT"
)

// Momentum scoring weights (sum = 100). Direction and order flow dominate; the
// trigger (breakout or pullback reclaim) confirms.
const (
	wTrend15m = 30.0 // 15分方向（強勢股閘門）
	wFlow     = 20.0 // 買盤單流
	wVolume   = 20.0 // 量能
	wTrend5m  = 15.0 // 5分動能
	wTrigger  = 15.0 // 3分突破 或 回測翻揚
)

const (
	breakoutVolRatio = 2.0 // 3m volume multiple required to call ignition
	fadeVolRatio     = 1.0 // 3m volume below this + declining = volume fade
	pullbackLookback = 6   // 5m bars to scan for a pullback dip
	exitScoreCap     = 25  // score ceiling once Level 3+ fires
)

// Config carries the BUY / WATCH score thresholds (from stocks.yaml).
type Config struct {
	BuyScore   int
	WatchScore int
}

// RadarSignal is the intraday momentum verdict for one stock.
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

	// Momentum core — the four questions + the graduated exit ladder.
	Momentum         string `json:"momentum"`           // STRONG / RISING / FADING / DEAD / NEUTRAL
	CanBuy           bool   `json:"can_buy"`            // 可以買嗎
	PullbackBuy      bool   `json:"pullback_buy"`       // 現在是回測買點嗎
	VolumeFade       bool   `json:"volume_fade"`        // 量能衰退（Level 1）
	BuyFlowWeakening bool   `json:"buy_flow_weakening"` // 買盤衰退
	Break5mSupport   bool   `json:"break_5m_support"`   // 5分跌破支撐
	MomentumFading   bool   `json:"momentum_fading"`    // 動能是否衰退
	ExitLevel        int    `json:"exit_level"`         // 0 = none .. 4 = force exit
	ShouldExit       bool   `json:"should_exit"`        // 是否應立即離場（Level 3+）

	// StopRef is a reference invalidation line (recent 5m swing low), shown for
	// context only. It is NOT a fixed stop — exits are momentum-driven.
	StopRef float64 `json:"stop_ref"`

	Score     int    `json:"score"`
	Action    string `json:"action"`
	MainForce string `json:"main_force"` // 主流股 / 短線炒作 / ""
	Closed    bool   `json:"closed"`     // intraday data is Yahoo-only (market shut)

	Reasons []string `json:"reasons"`
}

// Analyze produces the intraday momentum signal from aggregated bars and flow.
func Analyze(q *market.Quote, set market.IntradaySet, flow orderflow.Flow, cfg Config) RadarSignal {
	t15 := trendOf(set.Bars15m)
	t5 := trendOf(set.Bars5m)

	vol3 := volRatio(set.Bars3m)
	vol5 := volRatio(set.Bars5m)
	vol15 := volRatio(set.Bars15m)

	t3 := trend3m(set.Bars3m, vol3)

	uptrend := t15 == Bullish
	pullback := detectPullback(set.Bars5m, uptrend)

	// ── Momentum-fade detectors ───────────────────────────────
	volumeFade := detectVolumeFade(set.Bars3m, vol3, vol5)
	flowWeak := detectBuyFlowWeakening(flow)
	break5m := break5mSupport(set.Bars5m)

	exitLevel := computeExitLevel(t15, volumeFade, flowWeak, break5m)
	shouldExit := exitLevel >= 3
	momentumFading := volumeFade || flowWeak

	// ── Component scores (0..1) ───────────────────────────────
	c15 := trendComponent(t15)
	c5 := trendComponent(t5)
	cVol := volTrendComponent(vol3, vol5, vol15)
	cFlow := flowComponent(flow)
	cTrigger := math.Max(breakoutComponent(t3), boolComponent(pullback))

	score := int(math.Round(
		wTrend15m*c15 +
			wTrend5m*c5 +
			wVolume*cVol +
			wFlow*cFlow +
			wTrigger*cTrigger,
	))
	if shouldExit && score > exitScoreCap {
		score = exitScoreCap
	}

	momentum := classifyMomentum(t15, t5, flow, vol3, vol5, exitLevel, momentumFading)
	mainForce := classifyMainForce(vol3, vol5, vol15)
	action := decideAction(t15, t5, t3, score, flow, pullback, flowWeak, break5m, exitLevel, cfg)
	canBuy := action == ActionStrongBuy || action == ActionBuy || action == ActionPullbackBuy

	reasons := buildReasons(t15, t5, t3, flow, pullback, volumeFade, flowWeak, break5m, exitLevel, mainForce, vol3, vol5, vol15)

	return RadarSignal{
		Code:             q.Code,
		Name:             q.Name,
		Price:            q.Price,
		Trend15m:         t15,
		Trend5m:          t5,
		Trend3m:          t3,
		Volume15m:        vol15,
		Volume5m:         vol5,
		Volume3m:         vol3,
		Flow:             flow,
		Momentum:         momentum,
		CanBuy:           canBuy,
		PullbackBuy:      pullback,
		VolumeFade:       volumeFade,
		BuyFlowWeakening: flowWeak,
		Break5mSupport:   break5m,
		MomentumFading:   momentumFading,
		ExitLevel:        exitLevel,
		ShouldExit:       shouldExit,
		StopRef:          swingLow(set.Bars5m, pullbackLookback),
		Score:            score,
		Action:           action,
		MainForce:        mainForce,
		Closed:           set.Closed,
		Reasons:          reasons,
	}
}

// computeExitLevel maps the fade signals to the graduated exit ladder.
// Volume shrinking alone is only a profit-taking alert (Level 1); a 15m trend
// reversal forces you out regardless of volume (Level 4).
func computeExitLevel(t15 string, volumeFade, flowWeak, break5m bool) int {
	if t15 == Bearish {
		return 4
	}
	switch {
	case volumeFade && flowWeak && break5m:
		return 3
	case volumeFade && flowWeak:
		return 2
	case volumeFade:
		return 1
	default:
		return 0
	}
}

// decideAction applies the momentum action ladder. Exit levels are checked first
// (most severe wins), then entries, then the mild Level-1 profit alert and hold.
func decideAction(t15, t5, t3 string, score int, flow orderflow.Flow, pullback, flowWeak, break5m bool, exitLevel int, cfg Config) string {
	buyScore := cfg.BuyScore
	if buyScore == 0 {
		buyScore = 80
	}

	// Real breakdown / trend reversal — leave now.
	switch exitLevel {
	case 4:
		return ActionForceExit
	case 3:
		return ActionExit
	case 2:
		return ActionTakeProfit
	}

	// Strong-stock pullback entry: uptrend, healthy pullback reclaimed, buy flow
	// not weakening, support intact. Volume contraction on the dip is expected
	// here, so a Level-1 alert does not block it.
	if pullback && t15 == Bullish && !flowWeak && !break5m {
		return ActionPullbackBuy
	}

	// Fresh breakout entry still requires full alignment — a 3m spike alone never buys.
	gate := t15 == Bullish && t5 == Bullish && t3 == Breakout
	switch {
	case gate && score >= buyScore+7 && flow.Trend == orderflow.TrendStrengthening:
		return ActionStrongBuy
	case gate && score >= buyScore:
		return ActionBuy
	}

	// Level 1: volume shrinking → prepare to take profit.
	if exitLevel == 1 {
		return ActionTakeProfitAlert
	}

	// Momentum intact but no fresh trigger — ride it.
	if t15 == Bullish && t5 == Bullish {
		return ActionHold
	}
	return ActionWait
}

// detectPullback reports a healthy pullback-buy on the 5m frame: an intact
// uptrend that dipped toward MA20 support, held above it, and is now turning up.
func detectPullback(c []market.Candle, uptrend bool) bool {
	if !uptrend {
		return false
	}
	n := len(c)
	if n < 22 {
		return false
	}
	ma := strategy.MA(c, 20)
	if ma == 0 {
		return false
	}

	last := c[n-1]
	if last.Close <= ma || last.Close <= c[n-2].Close {
		return false // must have reclaimed MA and be turning up
	}

	start := n - 1 - pullbackLookback
	if start < 0 {
		start = 0
	}
	touched, deepBreak := false, false
	for i := start; i < n-1; i++ {
		if c[i].Low <= ma*1.01 {
			touched = true
		}
		if c[i].Close < ma*0.97 {
			deepBreak = true // closed well below MA = breakdown, not a clean pullback
		}
	}
	return touched && !deepBreak
}

// detectVolumeFade reports drying-up momentum volume: the current 3m bar is below
// its trailing average and the last three 3m bars are strictly declining, or both
// 3m and 5m volume are clearly contracted.
func detectVolumeFade(bars3m []market.Candle, vol3, vol5 float64) bool {
	if vol3 > 0 && vol3 < fadeVolRatio && decliningVolume(bars3m, 3) {
		return true
	}
	return vol3 > 0 && vol3 < 0.7 && vol5 > 0 && vol5 < 0.7
}

// detectBuyFlowWeakening reports fading buy-side pressure from the order-flow trend.
func detectBuyFlowWeakening(f orderflow.Flow) bool {
	if f.Trend == orderflow.TrendWeakening {
		return true
	}
	return f.Ratio > 0 && f.Ratio < 1.0 && f.RatioSlope < 0
}

// break5mSupport reports the 5m close dropping below its MA20 support.
func break5mSupport(c []market.Candle) bool {
	if len(c) < 22 {
		return false
	}
	ma := strategy.MA(c, 20)
	if ma == 0 {
		return false
	}
	return c[len(c)-1].Close < ma*0.997
}

// decliningVolume reports whether the last n bars have strictly decreasing volume.
func decliningVolume(c []market.Candle, n int) bool {
	if len(c) < n+1 || n < 2 {
		return false
	}
	seg := c[len(c)-n:]
	for i := 1; i < len(seg); i++ {
		if seg[i].Volume >= seg[i-1].Volume {
			return false
		}
	}
	return true
}

// swingLow returns the lowest low of the last n bars — a reference invalidation
// line for the current move (not a fixed stop).
func swingLow(c []market.Candle, n int) float64 {
	if len(c) == 0 {
		return 0
	}
	start := len(c) - n
	if start < 0 {
		start = 0
	}
	lo := c[start].Low
	for _, b := range c[start+1:] {
		if b.Low > 0 && b.Low < lo {
			lo = b.Low
		}
	}
	return r2(lo)
}

func classifyMomentum(t15, t5 string, flow orderflow.Flow, vol3, vol5 float64, exitLevel int, fading bool) string {
	switch {
	case exitLevel >= 3:
		return MomDead
	case fading || exitLevel >= 1:
		return MomFading
	case t15 == Bullish && t5 == Bullish && flow.Trend == orderflow.TrendStrengthening && (vol3 >= 1.2 || vol5 >= 1.2):
		return MomStrong
	case t15 == Bullish && t5 == Bullish:
		return MomRising
	default:
		return MomNeutral
	}
}

func buildReasons(t15, t5, t3 string, flow orderflow.Flow, pullback, volumeFade, flowWeak, break5m bool, exitLevel int, mainForce string, vol3, vol5, vol15 float64) []string {
	var rs []string
	rs = append(rs, fmt.Sprintf("15分%s（方向）", zhTrend(t15)))
	rs = append(rs, fmt.Sprintf("5分%s（進場）", zhTrend(t5)))
	rs = append(rs, fmt.Sprintf("3分%s（點火）", zhTrend(t3)))
	if pullback {
		rs = append(rs, "回測短均守住並翻揚（回測買點）")
	}
	if flow.Trend != "" {
		rs = append(rs, fmt.Sprintf("買盤%s（買賣比 %.2f）", flow.Trend, flow.Ratio))
	}
	if volumeFade {
		rs = append(rs, "量能衰退（上攻動能不足）")
	}
	if flowWeak {
		rs = append(rs, "買盤衰退")
	}
	if break5m {
		rs = append(rs, "5分跌破支撐")
	}
	switch exitLevel {
	case 1:
		rs = append(rs, "Level 1：動能開始衰退，建議準備獲利了結")
	case 2:
		rs = append(rs, "Level 2：量縮且買盤退，建議獲利了結")
	case 3:
		rs = append(rs, "Level 3：跌破支撐，建議離場")
	case 4:
		rs = append(rs, "Level 4：15分趨勢翻空，強制離場")
	}
	if mainForce != "" {
		rs = append(rs, fmt.Sprintf("%s（3分%.1fx／5分%.1fx／15分%.1fx）", mainForce, vol3, vol5, vol15))
	}
	return rs
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

// trend3m returns Breakout on a volume-confirmed break of the recent high,
// otherwise the ordinary MA-based trend.
func trend3m(c []market.Candle, vol3 float64) string {
	if isBreakout(c, vol3) {
		return Breakout
	}
	return trendOf(c)
}

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

// classifyMainForce distinguishes broad participation from a thin short-term ramp.
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

func volTrendComponent(vol3, vol5, vol15 float64) float64 {
	return (volScale(vol3) + volScale(vol5) + volScale(vol15)) / 3
}

func volScale(r float64) float64 {
	return clamp01((r - 0.8) / (2.0 - 0.8))
}

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

func boolComponent(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
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
