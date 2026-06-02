package strategy

import "stock-radar/internal/market"

// MA calculates the Simple Moving Average of Close prices over the last `period` candles.
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
