package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"stock-radar/internal/config"
	"stock-radar/internal/market"
	"stock-radar/internal/strategy"
)

const historyTTL = 4 * time.Hour

type Handler struct {
	cfg      *config.Config
	realtime market.RealtimeProvider
	history  market.HistoryProvider
	cache    *market.Cache

	histMu   sync.RWMutex
	histData map[string][]market.Candle
	histTime map[string]time.Time
}

// NewHandler pre-loads the realtime quote cache synchronously so that the first
// HTTP request is served instantly. History data is loaded lazily on demand.
func NewHandler(cfg *config.Config) *Handler {
	h := &Handler{
		cfg:      cfg,
		realtime: &market.TWSEProvider{},
		history:  &market.YahooHistoryProvider{},
		cache:    market.NewCache(),
		histData: make(map[string][]market.Candle),
		histTime: make(map[string]time.Time),
	}
	h.cache.Refresh(h.codes(), h.realtime)
	return h
}

// codes returns all stock codes that need realtime quotes (watchlist + positions, deduplicated).
func (h *Handler) codes() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, s := range h.cfg.Watchlist {
		if _, ok := seen[s.Code]; !ok {
			seen[s.Code] = struct{}{}
			out = append(out, s.Code)
		}
	}
	for _, p := range h.cfg.Positions {
		if _, ok := seen[p.Code]; !ok {
			seen[p.Code] = struct{}{}
			out = append(out, p.Code)
		}
	}
	return out
}

// getCandles returns cached history or fetches it if missing / stale.
func (h *Handler) getCandles(code string) []market.Candle {
	h.histMu.RLock()
	if t, ok := h.histTime[code]; ok && time.Since(t) < historyTTL {
		c := h.histData[code]
		h.histMu.RUnlock()
		return c
	}
	h.histMu.RUnlock()

	candles, err := h.history.GetCandles(code)
	if err != nil {
		log.Printf("history code=%s err=%v", code, err)
		h.histMu.RLock()
		stale := h.histData[code]
		h.histMu.RUnlock()
		return stale
	}

	h.histMu.Lock()
	h.histData[code] = candles
	h.histTime[code] = time.Now()
	h.histMu.Unlock()
	return candles
}

type stockRow struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Change      float64 `json:"change"`
	ChangePct   float64 `json:"change_pct"`
	Score       int     `json:"score"`
	Action      string  `json:"action"`
	MA5         float64 `json:"ma5"`
	MA20        float64 `json:"ma20"`
	RSI14       float64 `json:"rsi14"`
	VolRatio    float64 `json:"vol_ratio"`
	VolPattern  string  `json:"vol_pattern"`
	LargeOrder  bool    `json:"large_order"`
	BidAskRatio float64 `json:"bid_ask_ratio"`
}

// ListStocks serves the watchlist / scanner data.
func (h *Handler) ListStocks(w http.ResponseWriter, r *http.Request) {
	codes := h.codes()

	switch {
	case h.cache.IsEmpty():
		h.cache.Refresh(codes, h.realtime)
	case h.cache.IsStale():
		h.cache.RefreshAsync(codes, h.realtime)
	}

	snap, _ := h.cache.Snapshot()
	cfg := strategy.Config{
		BuyScore:   h.cfg.Strategy.BuyScore,
		WatchScore: h.cfg.Strategy.WatchScore,
	}

	out := make([]stockRow, 0, len(h.cfg.Watchlist))
	for _, s := range h.cfg.Watchlist {
		quote, ok := snap[s.Code]
		if !ok {
			continue
		}
		candles := h.getCandles(s.Code)
		sig := strategy.Analyze(quote, candles, cfg)
		out = append(out, stockRow{
			Code:        s.Code,
			Name:        s.Name,
			Price:       quote.Price,
			Change:      quote.Change,
			ChangePct:   quote.ChangePct,
			Score:       sig.Score,
			Action:      sig.Action,
			MA5:         sig.MA5,
			MA20:        sig.MA20,
			RSI14:       sig.RSI14,
			VolRatio:    sig.VolRatio,
			VolPattern:  sig.VolPattern,
			LargeOrder:  sig.LargeOrder,
			BidAskRatio: sig.BidAskRatio,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type positionRow struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Entry       float64  `json:"entry"`
	Shares      int      `json:"shares"`
	Current     float64  `json:"current"`
	Change      float64  `json:"change"`
	ChangePct   float64  `json:"change_pct"`
	ProfitPct   float64  `json:"profit_pct"`
	Score       int      `json:"score"`
	Action      string   `json:"action"`
	Advice      string   `json:"advice"`
	StopLoss    float64  `json:"stop_loss"`
	Target1     float64  `json:"target1"`
	Target2     float64  `json:"target2"`
	RiskReward  float64  `json:"risk_reward"`
	MA5         float64  `json:"ma5"`
	MA20        float64  `json:"ma20"`
	RSI14       float64  `json:"rsi14"`
	VolRatio    float64  `json:"vol_ratio"`
	VolPattern  string   `json:"vol_pattern"`
	LargeOrder  bool     `json:"large_order"`
	BidAskRatio float64  `json:"bid_ask_ratio"`
	Reasons     []string `json:"reason"`
}

// ListPositions returns position analysis for all configured holdings.
func (h *Handler) ListPositions(w http.ResponseWriter, r *http.Request) {
	codes := h.codes()

	switch {
	case h.cache.IsEmpty():
		h.cache.Refresh(codes, h.realtime)
	case h.cache.IsStale():
		h.cache.RefreshAsync(codes, h.realtime)
	}

	snap, _ := h.cache.Snapshot()
	cfg := strategy.Config{
		BuyScore:   h.cfg.Strategy.BuyScore,
		WatchScore: h.cfg.Strategy.WatchScore,
	}

	out := make([]positionRow, 0, len(h.cfg.Positions))
	for _, p := range h.cfg.Positions {
		quote, ok := snap[p.Code]
		if !ok {
			continue
		}
		candles := h.getCandles(p.Code)
		sig := strategy.AnalyzePosition(quote, candles, p.Entry, cfg)

		out = append(out, positionRow{
			Code:        p.Code,
			Name:        p.Name,
			Entry:       p.Entry,
			Shares:      p.Shares,
			Current:     sig.Current,
			Change:      quote.Change,
			ChangePct:   quote.ChangePct,
			ProfitPct:   sig.ProfitPct,
			Score:       sig.Score,
			Action:      sig.Action,
			Advice:      sig.Advice,
			StopLoss:    sig.StopLoss,
			Target1:     sig.Target1,
			Target2:     sig.Target2,
			RiskReward:  sig.RiskReward,
			MA5:         sig.MA5,
			MA20:        sig.MA20,
			RSI14:       sig.RSI14,
			VolRatio:    sig.VolRatio,
			VolPattern:  sig.VolPattern,
			LargeOrder:  sig.LargeOrder,
			BidAskRatio: sig.BidAskRatio,
			Reasons:     sig.Reasons,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// AnalyzeStock returns a full technical analysis for a single stock code.
func (h *Handler) AnalyzeStock(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	quote, ok := h.cache.Get(code)
	if !ok {
		var err error
		quote, err = h.realtime.GetQuote(code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	candles := h.getCandles(code)
	sig := strategy.Analyze(quote, candles, strategy.Config{
		BuyScore:   h.cfg.Strategy.BuyScore,
		WatchScore: h.cfg.Strategy.WatchScore,
	})

	resp := map[string]any{
		"code":          quote.Code,
		"name":          quote.Name,
		"price":         quote.Price,
		"change":        quote.Change,
		"change_pct":    quote.ChangePct,
		"action":        sig.Action,
		"score":         sig.Score,
		"entry":         sig.Entry,
		"stop_loss":     sig.StopLoss,
		"take_profit":   sig.TakeProfit,
		"ma5":           sig.MA5,
		"ma20":          sig.MA20,
		"ma60":          sig.MA60,
		"k":             sig.K,
		"d":             sig.D,
		"j":             sig.J,
		"rsi14":         sig.RSI14,
		"vol_ratio":     sig.VolRatio,
		"vol_pattern":   sig.VolPattern,
		"large_order":   sig.LargeOrder,
		"bid_ask_ratio": sig.BidAskRatio,
		"avg_vol20":     sig.AvgVol20,
		"reasons":       sig.Reasons,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
