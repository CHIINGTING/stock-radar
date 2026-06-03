package strategy

import (
	"math"

	"stock-radar/internal/market"
)

// MA returns the simple moving average of the last `period` closes.
// Returns 0 if there are not enough candles.
func MA(candles []market.Candle, period int) float64 {
	if len(candles) < period {
		return 0
	}
	start := len(candles) - period
	var total float64
	for _, c := range candles[start:] {
		total += c.Close
	}
	return total / float64(period)
}

func MA5(candles []market.Candle) float64  { return MA(candles, 5) }
func MA20(candles []market.Candle) float64 { return MA(candles, 20) }
func MA60(candles []market.Candle) float64 { return MA(candles, 60) }

// MA120 requires ~6 months of history (range=6mo from Yahoo).
func MA120(candles []market.Candle) float64 { return MA(candles, 120) }

// MASlope returns the percentage change of MA(period) over the last `lookback` candles.
// Positive = rising MA; negative = falling MA.
// Returns 0 if there is not enough data.
func MASlope(candles []market.Candle, period, lookback int) float64 {
	if len(candles) < period+lookback {
		return 0
	}
	now := MA(candles, period)
	past := MA(candles[:len(candles)-lookback], period)
	if past == 0 {
		return 0
	}
	return (now - past) / past * 100
}

// HighN returns the highest close price in the last n candles.
// Returns 0 if there are not enough candles.
func HighN(candles []market.Candle, n int) float64 {
	if len(candles) == 0 {
		return 0
	}
	start := len(candles) - n
	if start < 0 {
		start = 0
	}
	hi := candles[start].Close
	for _, c := range candles[start+1:] {
		if c.Close > hi {
			hi = c.Close
		}
	}
	return hi
}

// ATRPct returns the 14-period Average True Range as a percentage of the last close.
// Returns 0 if there are not enough candles.
func ATRPct(candles []market.Candle, n int) float64 {
	if len(candles) < n+1 {
		return 0
	}
	start := len(candles) - n
	var totalTR float64
	for i := start; i < len(candles); i++ {
		c := candles[i]
		prev := candles[i-1].Close
		tr := c.High - c.Low
		if v := math.Abs(c.High - prev); v > tr {
			tr = v
		}
		if v := math.Abs(c.Low - prev); v > tr {
			tr = v
		}
		totalTR += tr
	}
	atr := totalTR / float64(n)
	last := candles[len(candles)-1].Close
	if last == 0 {
		return 0
	}
	return atr / last * 100
}

// PriceChangePct returns the percentage price change from n candles ago to today.
// Returns 0 if there are not enough candles.
func PriceChangePct(candles []market.Candle, n int) float64 {
	if len(candles) < n+1 {
		return 0
	}
	past := candles[len(candles)-n-1].Close
	now := candles[len(candles)-1].Close
	if past == 0 {
		return 0
	}
	return (now - past) / past * 100
}
