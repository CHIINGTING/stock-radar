package market

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TWSEProvider fetches realtime quotes from the TWSE MIS API.
// It tries the TSE (上市) market first, then falls back to OTC/TPEX (上櫃).
type TWSEProvider struct{}

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
	if q, err := p.getMIS(code, "tse"); err == nil {
		return q, nil
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
		return nil, fmt.Errorf("stock %s not found on %s", code, market)
	}

	s := result.MsgArray[0]

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
