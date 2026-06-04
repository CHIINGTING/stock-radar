package market

import (
	"errors"
	"strconv"
	"strings"
)

// errStockNotOnMarket means the queried market (tse/otc) returned a valid response
// saying this code does not trade there. Only this error justifies falling back to
// the other market. Transient failures (EOF, timeout, decode) must NOT switch
// market — they are retried on the same market next cycle.
//
// The live MIS client lives in twseclient.go (TwseClient); this file holds the
// shared response types and parsing helpers.
var errStockNotOnMarket = errors.New("stock not on market")

// misEntry is one quote record in the TWSE MIS msgArray.
type misEntry struct {
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
}

type misResponse struct {
	MsgArray []misEntry `json:"msgArray"`
}

// quoteFromMIS builds a Quote from one MIS entry.
func quoteFromMIS(s misEntry) *Quote {
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
	}
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
