package strategy

import (
	"fmt"
	"math"

	"stock-radar/internal/market"
)

// Stage represents the Weinstein market-cycle phase of a stock.
type Stage = string

const (
	StageBase         Stage = "BASE"
	StageBreakout     Stage = "BREAKOUT"
	StageUptrend      Stage = "UPTREND"
	StageLateUptrend  Stage = "LATE_UPTREND"
	StageDistribution Stage = "DISTRIBUTION"
)

// StageNames maps stage constants to Traditional Chinese labels.
var StageNames = map[Stage]string{
	StageBase:         "底部整理",
	StageBreakout:     "突破初期",
	StageUptrend:      "主升段中期",
	StageLateUptrend:  "主升段末期",
	StageDistribution: "出貨風險",
}

// ScannerSignal is the swing-trading (1-4 week) analysis result.
type ScannerSignal struct {
	Action       string   `json:"action"`
	Score        int      `json:"score"`
	Stage        Stage    `json:"stage"`
	StageZh      string   `json:"stage_zh"`
	Holding      string   `json:"holding"`
	Risk         string   `json:"risk"`    // LOW / MEDIUM / HIGH / VERY_HIGH
	RiskZh       string   `json:"risk_zh"` // 低 / 中 / 高 / 極高
	Sector       string   `json:"sector"`
	SectorRank   int      `json:"sector_rank"`
	SectorFlow   string   `json:"sector_flow"`
	RS           float64  `json:"rs"`
	Is60DayHigh  bool     `json:"is_60d_high"`
	Is120DayHigh bool     `json:"is_120d_high"`
	MA20         float64  `json:"ma20"`
	MA60         float64  `json:"ma60"`
	MA120        float64  `json:"ma120"`
	// Taiwan limit fields
	LimitStatus     LimitStatus       `json:"limit_status"`
	LimitStatusZh   string            `json:"limit_status_zh"`
	LimitUpDays5    int               `json:"limit_up_days_5"`
	LimitDownDays5  int               `json:"limit_down_days_5"`
	OpenLimitType   string            `json:"open_limit_type"`
	IsHot           bool              `json:"is_hot"`
	IsAvoid         bool              `json:"is_avoid"`
	IsConsolidating bool              `json:"is_consolidating"`
	Regulation      *RegulationStatus `json:"regulation,omitempty"`
	Reasons         []string          `json:"reasons"`
}

// ScannerAnalyze performs swing-trading analysis oriented around 1-4 week holds.
//
// Key differences from Analyze():
//   - Weights MA60 / MA120 heavily (trend alignment)
//   - Uses 60/120-day highs, breakout detection, volume trend
//   - Computes Relative Strength vs watchlist average
//   - Integrates Taiwan limit-up/down analysis (LimitAnalysis)
//   - Ignores intraday bid/ask noise
//
// benchmarkReturn is the watchlist average 40-day price return (%).
// limit carries Taiwan-specific limit/regulation data; pass nil to skip.
func ScannerAnalyze(
	q *market.Quote,
	candles []market.Candle,
	cfg Config,
	benchmarkReturn float64,
	limit *LimitAnalysis,
) ScannerSignal {
	price := q.Price

	ma20 := MA20(candles)
	ma60 := MA60(candles)
	ma120 := MA120(candles)

	ma60Slope := MASlope(candles, 60, 10)
	ma20Slope := MASlope(candles, 20, 5)

	high60 := HighN(candles, 60)
	high120 := HighN(candles, 120)
	is60High := high60 > 0 && price >= high60*0.98
	is120High := high120 > 0 && price >= high120*0.98

	atrPct := ATRPct(candles, 14)

	var rs float64
	if benchmarkReturn != 0 {
		ret40 := PriceChangePct(candles, 40)
		rs = r2(ret40 / benchmarkReturn)
	}

	sector := SectorOf(q.Code)
	stage := calcStage(price, ma20, ma60, ma60Slope, high60)

	// HOT_STOCK or LIMIT_UP_CONSOLIDATION promote stage to BREAKOUT
	if limit != nil && (limit.IsHot || limit.IsConsolidating) && stage == StageBase {
		stage = StageBreakout
	}

	// ── Scoring (swing-oriented) ─────────────────────────────

	score := 0
	var reasons []string

	// MA stack
	if ma120 > 0 && price > ma120 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("站上 MA120 (%.2f)", ma120))
	}
	if ma60 > 0 && price > ma60 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("站上 MA60 (%.2f)", ma60))
	}
	if ma20 > 0 && price > ma20 {
		score += 5
		reasons = append(reasons, fmt.Sprintf("站上 MA20 (%.2f)", ma20))
	}

	// MA directional momentum
	if ma60Slope > 0.3 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("MA60 上揚 (+%.2f%%/10日)", ma60Slope))
	} else if ma60Slope < -0.5 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf("MA60 下彎 (%.2f%%/10日)", ma60Slope))
	}
	if ma20Slope > 0.5 {
		score += 5
		reasons = append(reasons, fmt.Sprintf("MA20 上揚 (+%.2f%%/5日)", ma20Slope))
	}

	// Price milestones
	if is120High {
		score += 20
		reasons = append(reasons, "120日新高")
	} else if is60High {
		score += 15
		reasons = append(reasons, "60日新高")
	}

	// Breakout from consolidation
	if breakoutFromConsolidation(price, candles) {
		score += 10
		reasons = append(reasons, "突破整理區")
	}

	// Volume trend (5-day avg vs 20-day avg)
	volExp := volExpansionRatio(candles)
	if volExp >= 1.5 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("量能大幅擴張 (%.1fx)", volExp))
	} else if volExp >= 1.2 {
		score += 5
		reasons = append(reasons, fmt.Sprintf("量能增加 (%.1fx)", volExp))
	} else if volExp < 0.7 && volExp > 0 {
		score -= 5
		reasons = append(reasons, fmt.Sprintf("量能萎縮 (%.1fx)", volExp))
	}

	// Relative Strength
	if rs > 1.5 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("相對強度領先 (RS %.2f)", rs))
	} else if rs > 1.0 {
		score += 5
		reasons = append(reasons, fmt.Sprintf("相對強度優於大盤 (RS %.2f)", rs))
	} else if rs > 0 && rs < 0.7 {
		score -= 5
		reasons = append(reasons, fmt.Sprintf("相對強度落後 (RS %.2f)", rs))
	}

	// MA60 overextension
	if ma60 > 0 {
		dev := (price/ma60 - 1) * 100
		if dev > 20 {
			score -= 10
			reasons = append(reasons, fmt.Sprintf("乖離 MA60 過大 (%.1f%%)", dev))
		}
	}

	// Price below MA60
	if ma60 > 0 && price < ma60 {
		score -= 20
		reasons = append(reasons, fmt.Sprintf("跌破 MA60 (%.2f)", ma60))
	}

	// Stage adjustments
	switch stage {
	case StageBreakout:
		score += 10
		reasons = append(reasons, "突破初期（波段最佳進場點）")
	case StageDistribution:
		score -= 15
		reasons = append(reasons, "出貨風險，避免追高")
	case StageLateUptrend:
		score -= 5
		reasons = append(reasons, "主升段末期，風險提升")
	}

	// ── Apply Taiwan limit analysis ──────────────────────────

	var limitStatus LimitStatus
	var limitStatusZh, openLimitType string
	var limitUp5, limitDown5 int
	isHot, isAvoid, isConsol := false, false, false
	var regulation *RegulationStatus

	if limit != nil {
		score += limit.ScoreAdjust
		reasons = append(reasons, limit.Reasons...)
		limitStatus = limit.TodayStatus
		limitStatusZh = limit.StatusZh
		openLimitType = limit.OpenLimitType
		limitUp5 = limit.LimitUpDays5
		limitDown5 = limit.LimitDownDays5
		isHot = limit.IsHot
		isAvoid = limit.IsAvoid
		isConsol = limit.IsConsolidating
		regulation = limit.Regulation

		// Force SELL if consecutive limit-down
		if isAvoid {
			stage = StageDistribution
		}
	}

	// ── Action ───────────────────────────────────────────────

	buyScore := cfg.BuyScore
	watchScore := cfg.WatchScore
	if buyScore == 0 {
		buyScore = 80
	}
	if watchScore == 0 {
		watchScore = 60
	}

	action := "SELL"
	switch {
	case isAvoid || stage == StageDistribution:
		action = "SELL"
	case score >= buyScore+15:
		action = "STRONG BUY"
	case score >= buyScore:
		action = "BUY"
	case score >= watchScore:
		action = "WATCH"
	case score >= 40:
		action = "HOLD"
	case score >= 20:
		action = "REDUCE"
	}

	// ── Risk ─────────────────────────────────────────────────

	risk := "MEDIUM"
	switch {
	case stage == StageBase || stage == StageBreakout:
		risk = "LOW"
	case stage == StageLateUptrend || atrPct > 4:
		risk = "HIGH"
	case stage == StageDistribution:
		risk = "VERY_HIGH"
	}

	// Apply risk floor from limit analysis
	if limit != nil {
		risk = maxRisk(risk, limit.RiskFloor)
	}

	riskZh := RiskZhMap[risk]
	if riskZh == "" {
		riskZh = risk
	}

	holding := calcHolding(stage, atrPct)

	return ScannerSignal{
		Action:          action,
		Score:           score,
		Stage:           stage,
		StageZh:         StageNames[stage],
		Holding:         holding,
		Risk:            risk,
		RiskZh:          riskZh,
		Sector:          sector,
		RS:              rs,
		Is60DayHigh:     is60High,
		Is120DayHigh:    is120High,
		MA20:            r2(ma20),
		MA60:            r2(ma60),
		MA120:           r2(ma120),
		LimitStatus:     limitStatus,
		LimitStatusZh:   limitStatusZh,
		LimitUpDays5:    limitUp5,
		LimitDownDays5:  limitDown5,
		OpenLimitType:   openLimitType,
		IsHot:           isHot,
		IsAvoid:         isAvoid,
		IsConsolidating: isConsol,
		Regulation:      regulation,
		Reasons:         reasons,
	}
}

// calcStage determines the Weinstein-inspired market cycle stage.
func calcStage(price, ma20, ma60, ma60Slope, high60 float64) Stage {
	if ma60 == 0 {
		return StageBase
	}
	ma60Dev := (price/ma60 - 1) * 100

	if price < ma60 && ma60Slope < -0.3 {
		return StageDistribution
	}
	if ma60Dev > 20 {
		return StageLateUptrend
	}
	if price > ma60 && ma60Slope > 0 && high60 > 0 && price >= high60*0.96 {
		return StageBreakout
	}
	if math.Abs(ma60Slope) <= 0.3 && math.Abs(ma60Dev) <= 8 {
		return StageBase
	}
	if price > ma20 && price > ma60 && ma60Slope > 0 {
		return StageUptrend
	}
	return StageBase
}

// calcHolding suggests a holding period based on stage and volatility.
func calcHolding(stage Stage, atrPct float64) string {
	base := map[Stage]string{
		StageBase:         "10-20天",
		StageBreakout:     "20-30天",
		StageUptrend:      "10-20天",
		StageLateUptrend:  "5-10天",
		StageDistribution: "觀望",
	}
	h := base[stage]
	if h == "觀望" {
		return h
	}
	if atrPct > 4 {
		switch h {
		case "20-30天":
			return "10-20天"
		case "10-20天":
			return "5-10天"
		}
	}
	return h
}

// breakoutFromConsolidation returns true when price broke above
// a recent 20-candle tight range (range width < 8%).
func breakoutFromConsolidation(price float64, candles []market.Candle) bool {
	n := len(candles)
	if n < 25 {
		return false
	}
	window := candles[n-21 : n-1]
	hi, lo := window[0].Close, window[0].Close
	for _, c := range window[1:] {
		if c.Close > hi {
			hi = c.Close
		}
		if c.Close < lo {
			lo = c.Close
		}
	}
	if lo == 0 {
		return false
	}
	return (hi-lo)/lo < 0.08 && price > hi
}

// volExpansionRatio returns 5-day average volume / 20-day average volume.
func volExpansionRatio(candles []market.Candle) float64 {
	n := len(candles)
	if n < 20 {
		return 1.0
	}
	var s5 int64
	for _, c := range candles[n-5:] {
		s5 += c.Volume
	}
	var s20 int64
	for _, c := range candles[n-20:] {
		s20 += c.Volume
	}
	avg20 := float64(s20) / 20
	if avg20 == 0 {
		return 1.0
	}
	return r2(float64(s5) / 5 / avg20)
}
