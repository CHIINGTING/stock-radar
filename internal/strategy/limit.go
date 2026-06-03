package strategy

import (
	"fmt"
	"time"

	"stock-radar/internal/market"
)

// Taiwan daily price-limit threshold (general rule).
const (
	LimitUpThreshold   = 9.5  // >= 9.5% = limit-up
	LimitDownThreshold = -9.5 // <= -9.5% = limit-down
)

// LimitStatus describes today's limit-price situation.
type LimitStatus = string

const (
	LimitNone            LimitStatus = ""
	LimitStatusUp        LimitStatus = "LIMIT_UP"
	LimitStatusDown      LimitStatus = "LIMIT_DOWN"
	LimitStatusOpenUp    LimitStatus = "OPEN_LIMIT_UP"
	LimitStatusConsol    LimitStatus = "LIMIT_UP_CONSOLIDATION"
	LimitStatusHot       LimitStatus = "HOT_STOCK"
	LimitStatusAvoid     LimitStatus = "AVOID"
)

// OpenLimitSubType classifies how a limit-up board was broken.
type OpenLimitSubType = string

const (
	OpenLimitBigVol OpenLimitSubType = "爆量開板"
	OpenLimitStrong OpenLimitSubType = "強勢開板"
	OpenLimitNormal OpenLimitSubType = "正常換手"
)

// RegulationStatus holds TWSE attention / disposition information for a stock.
type RegulationStatus struct {
	Type          string  `json:"type"`           // "處置股" / "注意股"
	StartDate     string  `json:"start_date"`
	EndDate       string  `json:"end_date"`
	RemainingDays int     `json:"remaining_days"` // calendar days to end; 0 = ends today; -1 = already ended
	AvgGainAfter  float64 `json:"avg_gain_after"` // cited avg gain (%) in Taiwan after disposition release
}

// ParseRegulation builds a RegulationStatus from raw YAML strings.
// Returns nil when warn is empty.
func ParseRegulation(warn, warnStart, warnEnd string, now time.Time) *RegulationStatus {
	if warn == "" {
		return nil
	}
	reg := &RegulationStatus{
		Type:         warn,
		StartDate:    warnStart,
		EndDate:      warnEnd,
		AvgGainAfter: 8.0, // commonly-cited Taiwan post-disposition rebound average
	}
	if warnEnd != "" {
		end, err := time.Parse("2006-01-02", warnEnd)
		if err == nil {
			days := int(end.Sub(now).Hours() / 24)
			if days < 0 {
				days = -1
			}
			reg.RemainingDays = days
		}
	}
	return reg
}

// LimitAnalysis holds the full Taiwan limit-price event breakdown for one stock.
type LimitAnalysis struct {
	TodayStatus     LimitStatus       `json:"today_status"`
	StatusZh        string            `json:"status_zh"`
	TodayChangePct  float64           `json:"today_change_pct"`
	LimitUpDays5    int               `json:"limit_up_days_5"`
	LimitDownDays5  int               `json:"limit_down_days_5"`
	LimitUpDays10   int               `json:"limit_up_days_10"`
	OpenLimitType   OpenLimitSubType  `json:"open_limit_type"`
	IsHot           bool              `json:"is_hot"`           // HOT_STOCK flag
	IsAvoid         bool              `json:"is_avoid"`         // AVOID flag
	IsConsolidating bool              `json:"is_consolidating"` // post-limit consolidation
	ScoreAdjust     int               `json:"score_adjust"`     // net points to add to scanner score
	RiskFloor       string            `json:"risk_floor"`       // minimum risk level
	Regulation      *RegulationStatus `json:"regulation,omitempty"`
	Reasons         []string          `json:"reasons"`
}

// AnalyzeLimits performs Taiwan-specific limit-up/limit-down analysis.
// candles should be sorted oldest-first (Yahoo Finance default order).
func AnalyzeLimits(q *market.Quote, candles []market.Candle, reg *RegulationStatus) LimitAnalysis {
	pct := q.ChangePct

	up5   := countLimitDays(candles, 5,  LimitUpThreshold)
	down5 := countLimitDays(candles, 5,  LimitDownThreshold)
	up10  := countLimitDays(candles, 10, LimitUpThreshold)

	var todayStatus LimitStatus
	var statusZh, openType string
	var scoreAdj int
	riskFloor := ""
	isHot, isAvoid, isConsol := false, false, false
	var reasons []string

	// ── Today's price action ─────────────────────────────────

	switch {
	case pct >= LimitUpThreshold:
		todayStatus = LimitStatusUp
		statusZh = "漲停"
		scoreAdj += 5
		riskFloor = maxRisk(riskFloor, "HIGH")
		reasons = append(reasons, "今日漲停板")

	case pct <= LimitDownThreshold:
		todayStatus = LimitStatusDown
		statusZh = "跌停"
		scoreAdj -= 20
		riskFloor = "VERY_HIGH"
		reasons = append(reasons, "今日跌停板")

	case todayHitLimitButOpened(q):
		todayStatus = LimitStatusOpenUp
		openType = classifyOpenLimit(q, candles)
		statusZh = openType
		switch openType {
		case OpenLimitBigVol:
			scoreAdj -= 5
			reasons = append(reasons, "爆量開板（留意出貨壓力）")
		case OpenLimitStrong:
			scoreAdj += 10
			reasons = append(reasons, "強勢開板（籌碼換手健康）")
		default:
			scoreAdj += 3
			reasons = append(reasons, "開板換手（觀察後續量能）")
		}
	}

	// ── Consecutive limit-ups ────────────────────────────────

	if up5 >= 2 {
		isHot = true
		if todayStatus == "" {
			todayStatus = LimitStatusHot
			statusZh = "連板強勢"
		}
		scoreAdj += 15
		riskFloor = maxRisk(riskFloor, "HIGH")
		reasons = append(reasons, fmt.Sprintf("近5日 %d 次漲停（連板強勢）", up5))
	}

	// ── Consecutive limit-downs ──────────────────────────────

	if down5 >= 2 {
		isAvoid = true
		if todayStatus == "" || todayStatus == LimitStatusDown {
			todayStatus = LimitStatusAvoid
			statusZh = "連跌警示"
		}
		scoreAdj -= 30
		riskFloor = "VERY_HIGH"
		reasons = append(reasons, fmt.Sprintf("近5日 %d 次跌停（高度危險，建議避開）", down5))
	}

	// ── Post-limit consolidation ─────────────────────────────

	if up10 >= 1 && !isAvoid && todayStatus != LimitStatusUp {
		ma20v := MA20(candles)
		if ma20v > 0 && q.Price > ma20v && volExpansionRatio(candles) < 0.8 {
			isConsol = true
			if todayStatus == "" {
				todayStatus = LimitStatusConsol
				statusZh = "漲停後整理"
			}
			scoreAdj += 15
			reasons = append(reasons, "漲停後縮量整理（第二波候選）")
		}
	}

	// ── Regulation ───────────────────────────────────────────

	if reg != nil {
		switch reg.Type {
		case "處置股":
			riskFloor = maxRisk(riskFloor, "HIGH")
			scoreAdj -= 10
			switch {
			case reg.RemainingDays >= 0 && reg.RemainingDays <= 3:
				// Near release: contrarian setup
				scoreAdj += 5
				reasons = append(reasons, fmt.Sprintf(
					"⚠ 處置股解除倒數 %d 天（歷史均值+%.1f%%）",
					reg.RemainingDays, reg.AvgGainAfter))
			case reg.RemainingDays > 0:
				reasons = append(reasons, fmt.Sprintf(
					"⚠ 處置股（剩餘 %d 天）", reg.RemainingDays))
			default:
				reasons = append(reasons, "⚠ 處置股")
			}
		case "注意股":
			riskFloor = maxRisk(riskFloor, "MEDIUM")
			scoreAdj -= 5
			reasons = append(reasons, "⚠ 注意股")
		}
	}

	return LimitAnalysis{
		TodayStatus:     todayStatus,
		StatusZh:        statusZh,
		TodayChangePct:  pct,
		LimitUpDays5:    up5,
		LimitDownDays5:  down5,
		LimitUpDays10:   up10,
		OpenLimitType:   openType,
		IsHot:           isHot,
		IsAvoid:         isAvoid,
		IsConsolidating: isConsol,
		ScoreAdjust:     scoreAdj,
		RiskFloor:       riskFloor,
		Regulation:      reg,
		Reasons:         reasons,
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// countLimitDays counts candles in the last n historical days where the day's
// return (vs prior close) meets the threshold.  Positive threshold → limit-up;
// negative threshold → limit-down.
func countLimitDays(candles []market.Candle, n int, threshold float64) int {
	total := len(candles)
	if total < 2 {
		return 0
	}
	start := total - n
	if start < 1 {
		start = 1
	}
	count := 0
	for i := start; i < total; i++ {
		prev := candles[i-1].Close
		if prev == 0 {
			continue
		}
		pct := (candles[i].Close - prev) / prev * 100
		if threshold > 0 && pct >= threshold {
			count++
		} else if threshold < 0 && pct <= threshold {
			count++
		}
	}
	return count
}

// todayHitLimitButOpened returns true when today's high reached the limit-up
// price but the current (closing) price did not lock the limit.
func todayHitLimitButOpened(q *market.Quote) bool {
	if q.RefPrice == 0 || q.High == 0 {
		return false
	}
	limitUp := q.RefPrice * (1 + LimitUpThreshold/100)
	return q.High >= limitUp*0.998 && q.Price < limitUp*0.998
}

// classifyOpenLimit sub-categorises a limit-up open based on volume and
// intraday price position.
func classifyOpenLimit(q *market.Quote, candles []market.Candle) OpenLimitSubType {
	dayRange := q.High - q.Low
	if dayRange == 0 {
		return OpenLimitNormal
	}
	pricePos := (q.Price - q.Low) / dayRange

	// Volume check vs 20-day candle average
	if len(candles) >= 20 {
		var sum int64
		for _, c := range candles[len(candles)-20:] {
			sum += c.Volume
		}
		avg20 := float64(sum) / 20
		if avg20 > 0 && float64(q.Volume) >= avg20*3 {
			return OpenLimitBigVol
		}
	}
	if pricePos >= 0.65 {
		return OpenLimitStrong
	}
	return OpenLimitNormal
}

// ── Risk ordering ─────────────────────────────────────────────────────────────

var riskOrder = []string{"", "LOW", "MEDIUM", "HIGH", "VERY_HIGH"}

var RiskZhMap = map[string]string{
	"LOW":       "低",
	"MEDIUM":    "中",
	"HIGH":      "高",
	"VERY_HIGH": "極高",
}

// maxRisk returns the more severe of two risk strings.
func maxRisk(a, b string) string {
	if indexOfRisk(b) > indexOfRisk(a) {
		return b
	}
	return a
}

func indexOfRisk(r string) int {
	for i, v := range riskOrder {
		if v == r {
			return i
		}
	}
	return 1 // default to LOW
}
