package market

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// errSymbolNotFound means Yahoo gave a definitive "this symbol does not exist on
// this market" answer (HTTP 404 or an empty/error chart result). Only this error
// justifies probing the other market (TW → TWO). Transient failures such as 429
// rate-limiting or timeouts are returned as ordinary errors and must NOT trigger
// a market switch.
var errSymbolNotFound = errors.New("symbol not found")

// rawTTL is how long fetched 1-minute bars are reused before re-hitting Yahoo.
// The live tail is re-merged from the MIS quote on every call, so freshness does
// not depend on this — it only throttles the (rate-limited) Yahoo endpoint.
const rawTTL = 45 * time.Second

// twLoc is the Taiwan trading timezone (UTC+8). Defined as a fixed zone so the
// binary does not depend on the host tzdata being present.
var twLoc = time.FixedZone("CST", 8*3600)

// Taiwan regular-session bounds (minutes since midnight, local time).
const (
	sessionOpenMin  = 9 * 60          // 09:00
	sessionCloseMin = 13*60 + 30      // 13:30
)

// IntradaySet holds aggregated intraday candles for the three Radar timeframes.
// Closed is true when the live (MIS) tail could not be applied — i.e. the market
// is closed or the realtime quote was unavailable, so the data is Yahoo-only.
type IntradaySet struct {
	Bars3m  []Candle
	Bars5m  []Candle
	Bars15m []Candle
	Closed  bool
}

// IntradayProvider fetches and aggregates intraday candles for a stock.
//
// market is "TW" / "TWO" / "" (auto-detect), matching HistoryProvider. live is
// the caller's current realtime quote (already cached upstream) used to extend
// the most recent minute; pass nil to get Yahoo-only bars.
type IntradayProvider interface {
	GetBars(code, market string, live *Quote) (IntradaySet, error)
}

// YahooIntradayProvider builds intraday bars from two sources:
//
//   - Historical 1-minute candles from Yahoo (interval=1m&range=1d), covering the
//     session up to roughly one minute ago. These are cached for rawTTL to avoid
//     hammering Yahoo's rate-limited endpoint.
//   - A live "tail" bar synthesised from the caller-supplied MIS quote, re-merged
//     on every call so the most recent minute reflects the current price.
//
// The two are merged on the minute timestamp (the live quote overrides Yahoo for
// the same minute, otherwise appends), then aggregated to 3m / 5m / 15m.
type YahooIntradayProvider struct {
	mu         sync.Mutex
	discovered map[string]string    // code → "TW" | "TWO"
	raw        map[string]rawEntry  // code → cached 1m bars
}

type rawEntry struct {
	bars    []Candle
	fetched time.Time
}

func (p *YahooIntradayProvider) GetBars(code, market string, live *Quote) (IntradaySet, error) {
	bars1m, err := p.fetch1m(code, market)
	if err != nil {
		return IntradaySet{}, err
	}
	if len(bars1m) == 0 {
		return IntradaySet{Closed: true}, nil
	}

	closed := true
	if live != nil && live.Price > 0 && marketOpenNow() {
		bars1m = mergeLiveTail(bars1m, live)
		closed = false
	}

	return IntradaySet{
		Bars3m:  aggregate(bars1m, 3),
		Bars5m:  aggregate(bars1m, 5),
		Bars15m: aggregate(bars1m, 15),
		Closed:  closed,
	}, nil
}

// fetch1m returns the day's 1-minute candles, served from the rawTTL cache when
// fresh. It honours the configured market suffix and, when auto-detecting, only
// probes TWO if TW returns a definitive "not found" — never on transient errors.
func (p *YahooIntradayProvider) fetch1m(code, market string) ([]Candle, error) {
	// Serve cached raw bars if still fresh.
	p.mu.Lock()
	if p.raw == nil {
		p.raw = make(map[string]rawEntry)
	}
	if e, ok := p.raw[code]; ok && time.Since(e.fetched) < rawTTL {
		bars := e.bars
		p.mu.Unlock()
		return bars, nil
	}
	if p.discovered == nil {
		p.discovered = make(map[string]string)
	}
	cached := p.discovered[code]
	p.mu.Unlock()

	bars, err := p.fetchResolved(code, market, cached)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.raw[code] = rawEntry{bars: bars, fetched: time.Now()}
	p.mu.Unlock()
	return bars, nil
}

// fetchResolved performs the actual Yahoo fetch, resolving the market suffix.
func (p *YahooIntradayProvider) fetchResolved(code, market, discovered string) ([]Candle, error) {
	// Explicit market from config — never probe the other one.
	if market != "" {
		return p.fetchSymbol1m(YahooSymbol(code, market))
	}
	// Previously auto-detected — reuse it.
	if discovered != "" {
		return p.fetchSymbol1m(YahooSymbol(code, discovered))
	}

	// Auto-detect: try TSE first.
	c, err := p.fetchSymbol1m(YahooSymbol(code, "TW"))
	if err == nil && len(c) > 0 {
		p.remember(code, "TW")
		return c, nil
	}
	// Only fall through to TPEX when TW genuinely has no such symbol.
	// Transient failures (429 rate-limit, timeout, 5xx) keep TW and retry later.
	if !errors.Is(err, errSymbolNotFound) {
		return nil, err
	}

	c2, err2 := p.fetchSymbol1m(YahooSymbol(code, "TWO"))
	if err2 == nil && len(c2) > 0 {
		p.remember(code, "TWO")
	}
	return c2, err2
}

func (p *YahooIntradayProvider) remember(code, market string) {
	p.mu.Lock()
	if p.discovered == nil {
		p.discovered = make(map[string]string)
	}
	p.discovered[code] = market
	p.mu.Unlock()
}

func (p *YahooIntradayProvider) fetchSymbol1m(symbol string) ([]Candle, error) {
	url := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1m&range=1d",
		symbol,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Referer", "https://finance.yahoo.com/")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Definitive: this symbol does not exist on this market.
		return nil, fmt.Errorf("%s: %w", symbol, errSymbolNotFound)
	case resp.StatusCode != http.StatusOK:
		// Transient (429 rate-limit, 5xx, …) — retryable, do not switch market.
		return nil, fmt.Errorf("yahoo returned %d for %s", resp.StatusCode, symbol)
	}

	var result yahooChartResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s: %w", symbol, err)
	}
	if len(result.Chart.Result) == 0 {
		// Valid response, no data → treat as "not found on this market".
		return nil, fmt.Errorf("%s: %w", symbol, errSymbolNotFound)
	}

	r := result.Chart.Result[0]
	if len(r.Indicators.Quote) == 0 {
		return nil, fmt.Errorf("no quote indicators for %s", symbol)
	}

	q := r.Indicators.Quote[0]
	n := len(r.Timestamp)

	candles := make([]Candle, 0, n)
	for i := 0; i < n; i++ {
		if i >= len(q.Close) || q.Close[i] == nil {
			continue // gaps (no trade in that minute) are skipped
		}

		var open, high, low float64
		var vol int64
		if i < len(q.Open) && q.Open[i] != nil {
			open = *q.Open[i]
		}
		if i < len(q.High) && q.High[i] != nil {
			high = *q.High[i]
		}
		if i < len(q.Low) && q.Low[i] != nil {
			low = *q.Low[i]
		}
		if i < len(q.Volume) && q.Volume[i] != nil {
			vol = *q.Volume[i]
		}

		candles = append(candles, Candle{
			Date:   time.Unix(r.Timestamp[i], 0).In(twLoc),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  *q.Close[i],
			Volume: vol,
		})
	}

	return candles, nil
}

// mergeLiveTail folds the realtime MIS quote into the 1-minute series so the
// current minute reflects live price. It updates the last bar in place when the
// quote falls in the same minute, otherwise appends a fresh tail bar.
//
// Volume reconciliation is best-effort: Yahoo intraday volume is in shares while
// MIS reports cumulative day volume in lots (張, ×1000 shares). The live tail
// volume is estimated as the day total minus what Yahoo has already accounted
// for, clamped to ≥ 0. Price/close — the field the Radar trend logic depends on
// — is always exact; volume is approximate by design (accepted trade-off).
func mergeLiveTail(bars []Candle, q *Quote) []Candle {
	if len(bars) == 0 {
		return bars
	}

	now := time.Now().In(twLoc)
	minute := now.Truncate(time.Minute)

	// Estimate the live tail volume from the day's cumulative figures.
	var yahooDayVol int64
	for _, b := range bars {
		yahooDayVol += b.Volume
	}
	misDayVol := q.Volume * 1000 // lots → shares
	tailVol := misDayVol - yahooDayVol
	if tailVol < 0 {
		tailVol = 0
	}

	last := &bars[len(bars)-1]
	if last.Date.In(twLoc).Truncate(time.Minute).Equal(minute) {
		// Same minute → extend the existing (still-forming) bar.
		if q.High > last.High {
			last.High = q.High
		}
		if q.Low > 0 && q.Low < last.Low {
			last.Low = q.Low
		}
		last.Close = q.Price
		if tailVol > last.Volume {
			last.Volume = tailVol
		}
		return bars
	}

	// New minute → append a fresh tail bar seeded from the live quote.
	high, low := q.Price, q.Price
	if q.High > high {
		high = q.High
	}
	if q.Low > 0 && q.Low < low {
		low = q.Low
	}
	return append(bars, Candle{
		Date:   minute,
		Open:   q.Price,
		High:   high,
		Low:    low,
		Close:  q.Price,
		Volume: tailVol,
	})
}

// aggregate combines consecutive 1-minute bars into n-minute bars, aligned to
// session boundaries from 09:00 (so 3m bars start 09:00/09:03/…, 15m bars start
// 09:00/09:15/…). Input must be chronological.
func aggregate(bars []Candle, n int) []Candle {
	if n <= 1 || len(bars) == 0 {
		return bars
	}

	var out []Candle
	var cur Candle
	curKey := -1

	for _, b := range bars {
		k := barIndex(b.Date) / n
		if k != curKey {
			if curKey != -1 {
				out = append(out, cur)
			}
			cur = b
			curKey = k
			continue
		}
		if b.High > cur.High {
			cur.High = b.High
		}
		if b.Low < cur.Low {
			cur.Low = b.Low
		}
		cur.Close = b.Close
		cur.Volume += b.Volume
	}
	if curKey != -1 {
		out = append(out, cur)
	}
	return out
}

// barIndex returns the minute offset of t from the 09:00 session open (Taiwan
// time). Used to bucket bars into aligned aggregation windows.
func barIndex(t time.Time) int {
	lt := t.In(twLoc)
	return lt.Hour()*60 + lt.Minute() - sessionOpenMin
}

// marketOpenNow reports whether the Taiwan regular session is currently open
// (weekday, 09:00–13:30). Used to decide if a live tail bar should be applied.
func marketOpenNow() bool {
	now := time.Now().In(twLoc)
	switch now.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	mins := now.Hour()*60 + now.Minute()
	return mins >= sessionOpenMin && mins <= sessionCloseMin
}
