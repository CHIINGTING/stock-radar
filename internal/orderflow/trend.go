package orderflow

const (
	TrendStrengthening = "增強" // buying interest rising
	TrendFlat          = "持平"
	TrendWeakening     = "衰退" // buying interest falling
)

// slopeThreshold is the per-minute change in bid/ask ratio above which the trend
// is considered strengthening (or below its negative, weakening). 0.05/min means
// a ~0.75 ratio move over the full 15-minute window flips the verdict.
const slopeThreshold = 0.05

// minSamples is the number of snapshots required before a directional verdict is
// issued; below this the trend is reported flat to avoid noise on cold start.
const minSamples = 3

// Flow summarises the order-flow state derived from a snapshot series.
type Flow struct {
	Ratio      float64    `json:"ratio"`       // latest bid/ask ratio
	RatioSlope float64    `json:"ratio_slope"` // least-squares slope, ratio per minute
	Trend      string     `json:"trend"`       // 增強 / 持平 / 衰退
	Samples    int        `json:"samples"`
	Series     []Snapshot `json:"series"` // raw points, oldest first (for the UI)
}

// Analyze computes the order-flow trend from a chronological snapshot series.
//
// The trend is the least-squares slope of Ratio against time (in minutes). Using
// the whole window — not just the first and last points — keeps the verdict
// stable against a single noisy reading, which is the entire reason this is
// tracked over time instead of from one snapshot.
func Analyze(series []Snapshot) Flow {
	f := Flow{Series: series, Samples: len(series), Trend: TrendFlat}
	if len(series) == 0 {
		return f
	}

	f.Ratio = series[len(series)-1].Ratio

	if len(series) < minSamples {
		return f
	}

	f.RatioSlope = ratioSlopePerMinute(series)
	switch {
	case f.RatioSlope > slopeThreshold:
		f.Trend = TrendStrengthening
	case f.RatioSlope < -slopeThreshold:
		f.Trend = TrendWeakening
	}
	return f
}

// ratioSlopePerMinute fits a line to (minutes-since-start, ratio) and returns its
// slope. Returns 0 when the points share a single timestamp (zero variance in x).
func ratioSlopePerMinute(series []Snapshot) float64 {
	t0 := series[0].T
	n := float64(len(series))

	var sumX, sumY, sumXY, sumXX float64
	for _, s := range series {
		x := s.T.Sub(t0).Minutes()
		y := s.Ratio
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}

	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}
