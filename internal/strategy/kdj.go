package strategy

import "stock-radar/internal/market"

// KDJ calculates K, D, J values using a 9-period stochastic oscillator
// with 1/3 smoothing factor. Initial K and D seed at 50.
func KDJ(candles []market.Candle) (k, d, j float64) {
	const n = 9
	k, d = 50.0, 50.0

	if len(candles) < n {
		j = 3*k - 2*d
		return
	}

	for i := n - 1; i < len(candles); i++ {
		window := candles[i-n+1 : i+1]

		highest := window[0].High
		lowest := window[0].Low
		for _, c := range window[1:] {
			if c.High > highest {
				highest = c.High
			}
			if c.Low < lowest {
				lowest = c.Low
			}
		}

		var rsv float64
		if highest > lowest {
			rsv = (candles[i].Close - lowest) / (highest - lowest) * 100
		} else {
			rsv = 50
		}

		k = 2.0/3.0*k + 1.0/3.0*rsv
		d = 2.0/3.0*d + 1.0/3.0*k
	}

	j = 3*k - 2*d
	return
}

// RSI calculates the Relative Strength Index using Wilder's smoothing method.
// Returns 50 when there are not enough candles.
func RSI(candles []market.Candle, period int) float64 {
	if len(candles) < period+1 {
		return 50
	}

	// Seed: simple average of the first `period` changes
	start := len(candles) - period - 1
	var avgGain, avgLoss float64
	for i := start + 1; i <= start+period; i++ {
		diff := candles[i].Close - candles[i-1].Close
		if diff > 0 {
			avgGain += diff
		} else {
			avgLoss += -diff
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	// Wilder smoothing for any remaining candles
	for i := start + period + 1; i < len(candles); i++ {
		diff := candles[i].Close - candles[i-1].Close
		var gain, loss float64
		if diff > 0 {
			gain = diff
		} else {
			loss = -diff
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}
