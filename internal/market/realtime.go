package market

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// errStockNotOnMarket means the queried market (tse/otc) returned a valid response
// saying this code does not trade there. Only this error justifies falling back to
// the other market. Transient failures (EOF, timeout, decode) must NOT switch
// market — they are retried on the same market next cycle.
var errStockNotOnMarket = errors.New("stock not on market")

// TWSEProvider fetches realtime quotes from the TWSE MIS API.
// Set MarketHints to map stock codes to their known MIS market string ("tse" or "otc")
// to skip the automatic TSE→OTC fallback for known stocks.
type TWSEProvider struct {
	MarketHints map[string]string // code → "tse" | "otc"
}

type misResponse struct {
	MsgArray []struct {
		Code string `json:"c"`
		Name string `json:"n"`

		Price string `json:"z"` // 成交價
		Open  string `json:"o"`
		High  string `json:"h"`
		Low   string `json:"l"`

		Volume string `json:"v"`

		RefPrice string `json:"y"` // 昨收
		Bid      string `json:"b"` // 委買價 (5 levels, _ sep)
		Ask      string `json:"a"` // 委賣價 (5 levels, _ sep)
		BidVol   string `json:"g"` // 委買量 (5 levels, _ sep)
		AskVol   string `json:"f"` // 委賣量 (5 levels, _ sep)
	} `json:"msgArray"`
}

func (p *TWSEProvider) GetQuote(code string) (*Quote, error) {
	if hint, ok := p.MarketHints[code]; ok {
		return p.getMIS(code, hint)
	}
	// Auto-detect: TSE first. Only fall through to OTC/TPEX when TSE gives a
	// definitive "not on this market" answer — never on transient errors (EOF,
	// timeout), which would otherwise mask the real cause and double the load.
	q, err := p.getMIS(code, "tse")
	if err == nil {
		return q, nil
	}
	if !errors.Is(err, errStockNotOnMarket) {
		return nil, err
	}
	return p.getMIS(code, "otc")
}

func (p *TWSEProvider) getMIS(code, market string) (*Quote, error) {
	url := fmt.Sprintf(
		"https://mis.twse.com.tw/stock/api/getStockInfo.jsp?ex_ch=%s_%s.tw",
		market, code,
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	req.Header.Set("Referer", "https://mis.twse.com.tw/")
	req.Header.Set("Accept", "application/json,text/plain,*/*")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: false,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: false},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("url=%s err=%w", url, err)
	}
	defer resp.Body.Close()

	var result misResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(result.MsgArray) == 0 {
		return nil, fmt.Errorf("stock %s not on %s: %w", code, market, errStockNotOnMarket)
	}

	s := result.MsgArray[0]

	// The TWSE MIS API returns a placeholder entry (empty code, price "-") when a
	// stock does not trade on the queried market.  Treat that as "not found" so the
	// caller can fall through to the correct market.
	if strings.TrimSpace(s.Code) == "" {
		return nil, fmt.Errorf("stock %s not on %s (empty entry): %w", code, market, errStockNotOnMarket)
	}

	refPrice, _ := strconv.ParseFloat(strings.TrimSpace(s.RefPrice), 64)

	price := parsePrice(s.Price, s.Bid, s.Ask, refPrice)

	open, _ := strconv.ParseFloat(strings.TrimSpace(s.Open), 64)
	high, _ := strconv.ParseFloat(strings.TrimSpace(s.High), 64)
	low, _ := strconv.ParseFloat(strings.TrimSpace(s.Low), 64)
	vol, _ := strconv.ParseInt(strings.TrimSpace(s.Volume), 10, 64)

	change := price - refPrice
	var changePct float64
	if refPrice > 0 {
		changePct = change / refPrice * 100
	}

	return &Quote{
		Code:      s.Code,
		Name:      s.Name,
		Price:     price,
		Open:      open,
		High:      high,
		Low:       low,
		Volume:    vol,
		RefPrice:  refPrice,
		Change:    round2(change),
		ChangePct: round2(changePct),
		BidVol:    sumVolLevels(s.BidVol),
		AskVol:    sumVolLevels(s.AskVol),
	}, nil
}

func parsePrice(priceStr, bidStr, askStr string, refPrice float64) float64 {
	if p := strings.TrimSpace(priceStr); p != "" && p != "-" {
		if v, err := strconv.ParseFloat(p, 64); err == nil {
			return v
		}
	}

	bid := firstLevel(bidStr)
	ask := firstLevel(askStr)

	switch {
	case bid > 0 && ask > 0:
		return (bid + ask) / 2
	case bid > 0:
		return bid
	case ask > 0:
		return ask
	default:
		return refPrice
	}
}

func firstLevel(v string) float64 {
	if v == "" || v == "-" {
		return 0
	}
	parts := strings.Split(v, "_")
	if len(parts) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(parts[0], 64)
	return f
}

func sumVolLevels(s string) int64 {
	if s == "" || s == "-" {
		return 0
	}
	var total int64
	for _, part := range strings.Split(s, "_") {
		v, _ := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		total += v
	}
	return total
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
