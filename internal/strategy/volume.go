package strategy

import (
	"fmt"

	"stock-radar/internal/market"
)

// VolumeMetrics summarises today's volume characteristics against history.
type VolumeMetrics struct {
	AvgVol20    int64   `json:"avg_vol20"`
	VolRatio    float64 `json:"vol_ratio"`    // today / avg20
	Pattern     string  `json:"vol_pattern"`  // 價漲量增 / 價漲量縮 / 價跌量增 / 價跌量縮 / 平盤
	LargeOrder  bool    `json:"large_order"`  // vol_ratio >= 2.0
	BidAskRatio float64 `json:"bid_ask_ratio"` // bid queue / ask queue
}

// AnalyzeVolume builds volume metrics from the current quote and candle history.
func AnalyzeVolume(q *market.Quote, candles []market.Candle) VolumeMetrics {
	var avgVol20 int64
	if n := len(candles); n >= 20 {
		var total int64
		for _, c := range candles[n-20:] {
			total += c.Volume
		}
		avgVol20 = total / 20
	} else if n > 0 {
		var total int64
		for _, c := range candles {
			total += c.Volume
		}
		avgVol20 = total / int64(n)
	}

	var volRatio float64
	if avgVol20 > 0 && q.Volume > 0 {
		volRatio = r2(float64(q.Volume) / float64(avgVol20))
	}

	pattern := "平盤"
	if q.Price > q.RefPrice {
		switch {
		case volRatio >= 1.2:
			pattern = "價漲量增"
		case volRatio < 0.8:
			pattern = "價漲量縮"
		default:
			pattern = "價漲量平"
		}
	} else if q.Price < q.RefPrice {
		switch {
		case volRatio >= 1.2:
			pattern = "價跌量增"
		case volRatio < 0.8:
			pattern = "價跌量縮"
		default:
			pattern = "價跌量平"
		}
	}

	var bidAskRatio float64
	if q.AskVol > 0 {
		bidAskRatio = r2(float64(q.BidVol) / float64(q.AskVol))
	}

	return VolumeMetrics{
		AvgVol20:    avgVol20,
		VolRatio:    volRatio,
		Pattern:     pattern,
		LargeOrder:  volRatio >= 2.0,
		BidAskRatio: bidAskRatio,
	}
}

// volumeScore returns the score delta and reason strings contributed by volume.
func volumeScore(vm VolumeMetrics, priceUp bool) (int, []string) {
	score := 0
	var reasons []string

	switch vm.Pattern {
	case "價漲量增":
		score += 15
		reasons = append(reasons, "價漲量增（多方主動）")
	case "價漲量縮":
		score += 5
		reasons = append(reasons, "價漲量縮（量能待確認）")
	case "價跌量增":
		score -= 15
		reasons = append(reasons, "價跌量增（空方主動）")
	case "價跌量縮":
		score -= 5
		reasons = append(reasons, "價跌量縮（量能萎縮）")
	}

	if vm.LargeOrder {
		if priceUp {
			score += 10
			reasons = append(reasons, fmt.Sprintf("大單介入（量比 %.1fx）", vm.VolRatio))
		} else {
			score -= 5
			reasons = append(reasons, fmt.Sprintf("大單賣出（量比 %.1fx）", vm.VolRatio))
		}
	}

	if vm.BidAskRatio > 1.5 {
		score += 10
		reasons = append(reasons, fmt.Sprintf("買盤強勁（買賣比 %.2f）", vm.BidAskRatio))
	} else if vm.BidAskRatio > 0 && vm.BidAskRatio < 0.67 {
		score -= 10
		reasons = append(reasons, fmt.Sprintf("賣壓偏重（買賣比 %.2f）", vm.BidAskRatio))
	}

	return score, reasons
}
