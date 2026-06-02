package strategy

import (
	"fmt"
	"math"
	"strings"

	"stock-radar/internal/market"
)

// Config holds scoring thresholds read from stocks.yaml.
type Config struct {
	BuyScore   int
	WatchScore int
}

// Signal is the analysis result returned by Analyze (watchlist / scanner).
type Signal struct {
	Action      string  `json:"action"`
	Score       int     `json:"score"`
	Entry       float64 `json:"entry"`
	StopLoss    float64 `json:"stop_loss"`
	TakeProfit  float64 `json:"take_profit"`
	MA5         float64 `json:"ma5"`
	MA20        float64 `json:"ma20"`
	MA60        float64 `json:"ma60"`
	K           float64 `json:"k"`
	D           float64 `json:"d"`
	J           float64 `json:"j"`
	RSI14       float64 `json:"rsi14"`
	VolRatio    float64 `json:"vol_ratio"`
	VolPattern  string  `json:"vol_pattern"`
	LargeOrder  bool    `json:"large_order"`
	BidAskRatio float64 `json:"bid_ask_ratio"`
	AvgVol20    int64   `json:"avg_vol20"`
	Reasons     []string `json:"reasons"`
}

// PositionSignal is the analysis result for a held position.
type PositionSignal struct {
	Action      string   `json:"action"`
	Score       int      `json:"score"`
	Current     float64  `json:"current"`
	ProfitPct   float64  `json:"profit_pct"`
	StopLoss    float64  `json:"stop_loss"`
	Target1     float64  `json:"target1"`
	Target2     float64  `json:"target2"`
	RiskReward  float64  `json:"risk_reward"`
	Advice      string   `json:"advice"`
	MA5         float64  `json:"ma5"`
	MA20        float64  `json:"ma20"`
	MA60        float64  `json:"ma60"`
	K           float64  `json:"k"`
	D           float64  `json:"d"`
	J           float64  `json:"j"`
	RSI14       float64  `json:"rsi14"`
	VolRatio    float64  `json:"vol_ratio"`
	VolPattern  string   `json:"vol_pattern"`
	LargeOrder  bool     `json:"large_order"`
	BidAskRatio float64  `json:"bid_ask_ratio"`
	Reasons     []string `json:"reasons"`
}

// Analyze scores a stock using realtime quote + historical candles.
// Returns STRONG BUY / BUY / WATCH / WAIT plus volume signals.
func Analyze(q *market.Quote, candles []market.Candle, cfg Config) Signal {
	score := 0
	var reasons []string

	ma5 := MA5(candles)
	ma20 := MA20(candles)
	ma60 := MA60(candles)
	k, d, j := KDJ(candles)
	rsi14 := RSI(candles, 14)
	vm := AnalyzeVolume(q, candles)
	price := q.Price

	// Moving averages
	if ma5 > 0 && price > ma5 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("站上 MA5 (%.2f)", ma5))
	}
	if ma20 > 0 && price > ma20 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("站上 MA20 (%.2f)", ma20))
	}
	if ma60 > 0 && price > ma60 {
		score += 20
		reasons = append(reasons, fmt.Sprintf("站上 MA60 (%.2f)", ma60))
	}
	if ma5 > 0 && ma20 > 0 && ma5 > ma20 {
		score += 10
		reasons = append(reasons, "MA5 > MA20 多頭排列")
	}
	if ma20 > 0 && ma60 > 0 && ma20 > ma60 {
		score += 10
		reasons = append(reasons, "MA20 > MA60 多頭排列")
	}

	// KDJ
	if k < 30 && d < 30 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("KD 超賣區 K=%.1f D=%.1f", k, d))
	} else if k > 80 && d > 80 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf("KD 超買區 K=%.1f D=%.1f", k, d))
	}
	if k > d && j > k {
		score += 10
		reasons = append(reasons, fmt.Sprintf("KDJ 黃金交叉 K=%.1f D=%.1f", k, d))
	}

	// RSI
	if rsi14 < 30 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("RSI 超賣 (%.1f)", rsi14))
	} else if rsi14 > 70 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf("RSI 超買 (%.1f)", rsi14))
	}

	// Intraday position
	if q.Open > 0 && price > q.Open {
		score += 5
		reasons = append(reasons, "站上開盤價")
	}
	if q.High > q.Low && price > (q.High+q.Low)/2 {
		score += 5
		reasons = append(reasons, "位於今日區間上半部")
	}

	// Volume
	vs, vr := volumeScore(vm, price > q.RefPrice)
	score += vs
	reasons = append(reasons, vr...)

	// Defaults
	buyScore := cfg.BuyScore
	watchScore := cfg.WatchScore
	if buyScore == 0 {
		buyScore = 80
	}
	if watchScore == 0 {
		watchScore = 60
	}

	action := "WAIT"
	if score >= watchScore {
		action = "WATCH"
	}
	if score >= buyScore {
		action = "BUY"
	}
	if score >= buyScore+15 {
		action = "STRONG BUY"
	}

	return Signal{
		Action:      action,
		Score:       score,
		Entry:       price,
		StopLoss:    r2(price * 0.97),
		TakeProfit:  r2(price * 1.06),
		MA5:         r2(ma5),
		MA20:        r2(ma20),
		MA60:        r2(ma60),
		K:           r2(k),
		D:           r2(d),
		J:           r2(j),
		RSI14:       r2(rsi14),
		VolRatio:    vm.VolRatio,
		VolPattern:  vm.Pattern,
		LargeOrder:  vm.LargeOrder,
		BidAskRatio: vm.BidAskRatio,
		AvgVol20:    vm.AvgVol20,
		Reasons:     reasons,
	}
}

// AnalyzePosition evaluates a held position and returns a trading recommendation.
// Actions: STRONG BUY / HOLD / REDUCE / TAKE PROFIT / STOP LOSS / SELL
func AnalyzePosition(q *market.Quote, candles []market.Candle, entry float64, cfg Config) PositionSignal {
	ma5 := MA5(candles)
	ma20 := MA20(candles)
	ma60 := MA60(candles)
	k, d, j := KDJ(candles)
	rsi14 := RSI(candles, 14)
	vm := AnalyzeVolume(q, candles)

	price := q.Price
	profitPct := (price - entry) / entry * 100

	// Stop loss: tighter of MA20-based and fixed 5%.
	fixedStop := entry * 0.95
	stopLoss := fixedStop
	if ma20 > 0 && ma20 < entry {
		techStop := ma20 * 0.995
		if techStop > fixedStop {
			stopLoss = techStop
		}
	}
	stopLoss = r2(stopLoss)

	risk := entry - stopLoss
	if risk <= 0 {
		risk = entry * 0.05
	}
	target1 := r2(entry + risk*1.5)
	target2 := r2(entry + risk*3.0)
	riskReward := r2((target1 - entry) / risk)

	// Technical score
	score := 0
	if ma5 > 0 && price > ma5 {
		score += 10
	}
	if ma20 > 0 && price > ma20 {
		score += 15
	}
	if ma60 > 0 && price > ma60 {
		score += 20
	}
	if ma5 > 0 && ma20 > 0 && ma5 > ma20 {
		score += 10
	}
	if ma20 > 0 && ma60 > 0 && ma20 > ma60 {
		score += 10
	}
	if k < 30 && d < 30 {
		score += 15
	} else if k > 80 && d > 80 {
		score -= 10
	}
	if k > d && j > k {
		score += 10
	}
	if rsi14 < 30 {
		score += 15
	} else if rsi14 > 70 {
		score -= 10
	}
	if q.Open > 0 && price > q.Open {
		score += 5
	}
	if q.High > q.Low && price > (q.High+q.Low)/2 {
		score += 5
	}
	vs, _ := volumeScore(vm, price > q.RefPrice)
	score += vs

	// Collect bearish signals
	var stopReasons []string
	if price <= stopLoss {
		stopReasons = append(stopReasons, fmt.Sprintf("觸及停損價 (%.2f)", stopLoss))
	}
	if ma20 > 0 && price < ma20 {
		stopReasons = append(stopReasons, fmt.Sprintf("跌破 MA20 (%.2f)", ma20))
	}
	if k < d && j < d && k < 50 {
		stopReasons = append(stopReasons, fmt.Sprintf("KDJ 死亡交叉 K=%.1f D=%.1f", k, d))
	}
	if rsi14 < 40 && profitPct < 0 {
		stopReasons = append(stopReasons, fmt.Sprintf("RSI 偏弱 (%.1f)", rsi14))
	}
	if vm.Pattern == "價跌量增" {
		stopReasons = append(stopReasons, "價跌量增（空方主動）")
	}

	// Collect bullish/profit signals
	var profitReasons []string
	if price >= target2 {
		profitReasons = append(profitReasons, fmt.Sprintf("已達目標二 (%.2f)", target2))
	} else if price >= target1 {
		profitReasons = append(profitReasons, fmt.Sprintf("已達目標一 (%.2f)", target1))
	}
	if k > 80 && d > 80 {
		profitReasons = append(profitReasons, fmt.Sprintf("KD 超買 K=%.1f D=%.1f", k, d))
	}
	if rsi14 > 70 {
		profitReasons = append(profitReasons, fmt.Sprintf("RSI 超買 (%.1f)", rsi14))
	}

	// Collect hold reasons
	var holdReasons []string
	if ma20 > 0 && price > ma20 {
		holdReasons = append(holdReasons, fmt.Sprintf("站穩 MA20 (%.2f)", ma20))
	}
	if ma5 > 0 && ma20 > 0 && ma5 > ma20 {
		holdReasons = append(holdReasons, "MA5 > MA20 多頭排列")
	}
	if ma60 > 0 && price > ma60 {
		holdReasons = append(holdReasons, fmt.Sprintf("站上 MA60 (%.2f)", ma60))
	}
	if vm.Pattern == "價漲量增" {
		holdReasons = append(holdReasons, "價漲量增（多方主動）")
	}
	if len(holdReasons) == 0 {
		holdReasons = append(holdReasons, "持倉訊號中性，繼續觀察")
	}

	// Determine action (priority: SELL > STOP LOSS > TAKE PROFIT > REDUCE > STRONG BUY > HOLD)
	action := "HOLD"
	var reasons []string

	buyScore := cfg.BuyScore
	if buyScore == 0 {
		buyScore = 80
	}

	hardStop := price <= stopLoss
	switch {
	case hardStop && len(stopReasons) >= 2:
		// Hard stop + technical confirmation = full exit
		action = "SELL"
		reasons = stopReasons
	case hardStop || len(stopReasons) >= 2:
		action = "STOP LOSS"
		reasons = stopReasons
	case price >= target1:
		action = "TAKE PROFIT"
		reasons = profitReasons
	case profitPct > 5 && len(profitReasons) >= 2:
		// Overbought with meaningful profit but not at target → reduce
		action = "REDUCE"
		reasons = profitReasons
	case score >= buyScore && profitPct >= 0 && vm.Pattern == "價漲量增":
		action = "STRONG BUY"
		reasons = append(holdReasons, fmt.Sprintf("技術評分強勢 (%d)，量增價漲，可考慮加碼", score))
	default:
		action = "HOLD"
		reasons = holdReasons
	}

	advice := buildAdvice(action, stopLoss, target1, target2, reasons)

	return PositionSignal{
		Action:      action,
		Score:       score,
		Current:     r2(price),
		ProfitPct:   r2(profitPct),
		StopLoss:    stopLoss,
		Target1:     target1,
		Target2:     target2,
		RiskReward:  riskReward,
		Advice:      advice,
		MA5:         r2(ma5),
		MA20:        r2(ma20),
		MA60:        r2(ma60),
		K:           r2(k),
		D:           r2(d),
		J:           r2(j),
		RSI14:       r2(rsi14),
		VolRatio:    vm.VolRatio,
		VolPattern:  vm.Pattern,
		LargeOrder:  vm.LargeOrder,
		BidAskRatio: vm.BidAskRatio,
		Reasons:     reasons,
	}
}

func buildAdvice(action string, stopLoss, target1, target2 float64, reasons []string) string {
	rt := strings.Join(reasons, "，")
	switch action {
	case "SELL":
		return fmt.Sprintf("建議出清。%s。已跌破停損且技術面全面轉弱，請立即出場，損失控制優先。", rt)
	case "STOP LOSS":
		return fmt.Sprintf("建議停損。%s。請於停損價 %.2f 附近出場，切勿攤平，避免損失擴大。", rt, stopLoss)
	case "TAKE PROFIT":
		return fmt.Sprintf("建議獲利了結。%s。已達目標，可分批出場鎖定利潤，或移動停損至成本以上保護獲利。", rt)
	case "REDUCE":
		return fmt.Sprintf("建議適度減碼。%s。可先出脫 1/3〜1/2 持股保留獲利，剩餘部位守至目標二 %.2f。", rt, target2)
	case "STRONG BUY":
		return fmt.Sprintf("可考慮加碼。%s。停損維持在 %.2f，加碼前確認量能是否持續。", rt, stopLoss)
	default:
		return fmt.Sprintf("建議續抱。%s。持有至目標一 %.2f，嚴守停損 %.2f，勿提前出場。", rt, target1, stopLoss)
	}
}

func r2(v float64) float64 {
	return math.Round(v*100) / 100
}
