package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// YahooSymbol converts a code and market suffix to a Yahoo Finance symbol.
//
//	YahooSymbol("2337", "TW")  → "2337.TW"   (TSE/上市)
//	YahooSymbol("5483", "TWO") → "5483.TWO"  (TPEX/上櫃)
func YahooSymbol(code, market string) string {
	return code + "." + market
}

// YahooHistoryProvider fetches daily candles from Yahoo Finance.
//
// Market discovery is cached: once a code is resolved to "TW" or "TWO" the
// result is stored in memory so subsequent calls never retry the wrong suffix.
type YahooHistoryProvider struct {
	// Client overrides the default Yahoo HTTP client (e.g. to force HTTP/1.1).
	// nil falls back to the package default.
	Client *http.Client

	mu         sync.Mutex
	discovered map[string]string // code → "TW" | "TWO"
}

func (p *YahooHistoryProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return yahooClient
}

type yahooChartResp struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*int64   `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error interface{} `json:"error"`
	} `json:"chart"`
}

// GetCandles fetches 3-month daily candles for code.
//
//   - market = "TW"  → only tries TSE symbol (code.TW)
//   - market = "TWO" → only tries TPEX symbol (code.TWO)
//   - market = ""    → auto-detect: tries TW first, falls back to TWO;
//     the winning suffix is cached so future calls skip the probe.
func (p *YahooHistoryProvider) GetCandles(code, market string) ([]Candle, error) {
	// Explicit market — skip cache and probe entirely.
	if market != "" {
		return p.fetch(YahooSymbol(code, market))
	}

	// Check discovery cache.
	p.mu.Lock()
	if p.discovered == nil {
		p.discovered = make(map[string]string)
	}
	cached := p.discovered[code]
	p.mu.Unlock()

	if cached != "" {
		return p.fetch(YahooSymbol(code, cached))
	}

	// Probe TW → TWO and cache the winner.
	if c, err := p.fetch(YahooSymbol(code, "TW")); err == nil && len(c) > 0 {
		p.mu.Lock()
		p.discovered[code] = "TW"
		p.mu.Unlock()
		return c, nil
	}

	c, err := p.fetch(YahooSymbol(code, "TWO"))
	if err == nil && len(c) > 0 {
		p.mu.Lock()
		p.discovered[code] = "TWO"
		p.mu.Unlock()
	}
	return c, err
}

func (p *YahooHistoryProvider) fetch(symbol string) ([]Candle, error) {
	url := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=6mo",
		symbol,
	)

	status, body, err := httpGet(p.client(), url, yahooHeaders())
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", symbol, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("yahoo returned %d for %s", status, symbol)
	}

	var result yahooChartResp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode %s: %w", symbol, err)
	}
	if len(result.Chart.Result) == 0 {
		return nil, fmt.Errorf("no chart data for %s", symbol)
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
			continue
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
			Date:   time.Unix(r.Timestamp[i], 0),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  *q.Close[i],
			Volume: vol,
		})
	}

	return candles, nil
}
