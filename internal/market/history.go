package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// YahooHistoryProvider fetches daily candles from Yahoo Finance.
// Taiwan TSE stocks use the ".TW" suffix; TPEX stocks use ".TWO".
type YahooHistoryProvider struct{}

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

func (p *YahooHistoryProvider) GetCandles(code string) ([]Candle, error) {
	if c, err := p.fetch(code + ".TW"); err == nil && len(c) > 0 {
		return c, nil
	}
	return p.fetch(code + ".TWO")
}

func (p *YahooHistoryProvider) fetch(symbol string) ([]Candle, error) {
	url := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=3mo",
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yahoo returned %d for %s", resp.StatusCode, symbol)
	}

	var result yahooChartResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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
