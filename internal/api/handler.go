package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"stock-radar/internal/config"
	"stock-radar/internal/market"
	"stock-radar/internal/orderflow"
	"stock-radar/internal/radar"
	"stock-radar/internal/strategy"
)

const (
	historyTTL = 4 * time.Hour

	// pollInterval is the minimum gap between background quote refreshes that
	// feed the order-flow recorder. A full refresh already takes several seconds
	// (rate-limited worker pool), so this keeps a steady ~per-30s sampling cadence
	// for the 15-minute order-flow window without hammering the MIS API.
	pollInterval = 10 * time.Second
)

type Handler struct {
	cfg      *config.Config
	realtime market.RealtimeProvider
	history  market.HistoryProvider
	intraday market.IntradayProvider
	cache    *market.Cache
	flow     *orderflow.Recorder

	// markets maps each stock code to its configured market suffix ("TW"/"TWO"/empty).
	// Empty means the history provider will auto-detect and cache the result.
	markets map[string]string

	histMu   sync.RWMutex
	histData map[string][]market.Candle
	histTime map[string]time.Time

	// intraData holds the last good intraday set per code, returned as a fallback
	// when a fetch transiently fails.
	intraMu   sync.RWMutex
	intraData map[string]market.IntradaySet
}

// NewHandler pre-loads the realtime quote cache synchronously so that the first
// HTTP request is served instantly. History data is loaded lazily on demand.
func NewHandler(cfg *config.Config) *Handler {
	// Build market-hint maps from config.
	markets := make(map[string]string)
	misHints := make(map[string]string) // code → "tse" | "otc" for TWSE provider

	for _, s := range cfg.Watchlist {
		markets[s.Code] = s.Market
		if h := yahootoMIS(s.Market); h != "" {
			misHints[s.Code] = h
		}
	}
	for _, p := range cfg.Positions {
		markets[p.Code] = p.Market
		if h := yahootoMIS(p.Market); h != "" {
			misHints[p.Code] = h
		}
	}

	realtime := &market.TWSEProvider{MarketHints: misHints}

	h := &Handler{
		cfg:       cfg,
		realtime:  realtime,
		history:   &market.YahooHistoryProvider{},
		intraday:  &market.YahooIntradayProvider{},
		cache:     market.NewCache(),
		flow:      orderflow.NewRecorder(),
		markets:   markets,
		histData:  make(map[string][]market.Candle),
		histTime:  make(map[string]time.Time),
		intraData: make(map[string]market.IntradaySet),
	}
	h.cache.Refresh(h.codes(), h.realtime)
	h.recordFlow() // seed the first order-flow sample
	return h
}

// StartPolling launches a background loop that periodically refreshes realtime
// quotes and feeds the order-flow recorder, so the 15-minute bid/ask series is
// populated continuously regardless of which endpoints are being hit.
func (h *Handler) StartPolling() {
	go func() {
		for {
			h.cache.Refresh(h.codes(), h.realtime)
			h.recordFlow()
			time.Sleep(pollInterval)
		}
	}()
}

// recordFlow snapshots the current bid/ask queues for every code into the recorder.
func (h *Handler) recordFlow() {
	snap, _ := h.cache.Snapshot()
	now := time.Now()
	for code, q := range snap {
		h.flow.Record(code, q.BidVol, q.AskVol, now)
	}
}

// getIntraday fetches intraday bars for code, merging the supplied live quote as
// the latest minute. The provider throttles the Yahoo endpoint internally; on a
// transient fetch error this returns the last good set so the UI keeps rendering.
func (h *Handler) getIntraday(code string, live *market.Quote) (market.IntradaySet, bool) {
	set, err := h.intraday.GetBars(code, h.markets[code], live)
	if err != nil {
		log.Printf("intraday code=%s err=%v", code, err)
		h.intraMu.RLock()
		stale, ok := h.intraData[code]
		h.intraMu.RUnlock()
		return stale, ok
	}

	h.intraMu.Lock()
	h.intraData[code] = set
	h.intraMu.Unlock()
	return set, true
}

// yahootoMIS converts a Yahoo market suffix to the TWSE MIS market string.
func yahootoMIS(yahooMarket string) string {
	switch yahooMarket {
	case "TW":
		return "tse"
	case "TWO":
		return "otc"
	default:
		return ""
	}
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
// The configured market suffix for code (if any) is forwarded to the provider
// so it never probes the wrong Yahoo symbol.
func (h *Handler) getCandles(code string) []market.Candle {
	h.histMu.RLock()
	if t, ok := h.histTime[code]; ok && time.Since(t) < historyTTL {
		c := h.histData[code]
		h.histMu.RUnlock()
		return c
	}
	h.histMu.RUnlock()

	mkt := h.markets[code] // "" if not configured → auto-detect
	candles, err := h.history.GetCandles(code, mkt)
	if err != nil {
		log.Printf("history code=%s market=%q err=%v", code, mkt, err)
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

// ── Scanner ───────────────────────────────────────────────────────────────────

type scannerRow struct {
	Code            string                    `json:"code"`
	Name            string                    `json:"name"`
	Price           float64                   `json:"price"`
	Change          float64                   `json:"change"`
	ChangePct       float64                   `json:"change_pct"`
	Score           int                       `json:"score"`
	Action          string                    `json:"action"`
	Stage           string                    `json:"stage"`
	StageZh         string                    `json:"stage_zh"`
	Holding         string                    `json:"holding"`
	Risk            string                    `json:"risk"`
	RiskZh          string                    `json:"risk_zh"`
	Sector          string                    `json:"sector"`
	SectorRank      int                       `json:"sector_rank"`
	SectorFlow      string                    `json:"sector_flow"`
	RS              float64                   `json:"rs"`
	Is60DayHigh     bool                      `json:"is_60d_high"`
	Is120DayHigh    bool                      `json:"is_120d_high"`
	MA20            float64                   `json:"ma20"`
	MA60            float64                   `json:"ma60"`
	MA120           float64                   `json:"ma120"`
	// Taiwan limit analysis
	LimitStatus     strategy.LimitStatus      `json:"limit_status"`
	LimitStatusZh   string                    `json:"limit_status_zh"`
	LimitUpDays5    int                       `json:"limit_up_days_5"`
	LimitDownDays5  int                       `json:"limit_down_days_5"`
	OpenLimitType   string                    `json:"open_limit_type"`
	IsHot           bool                      `json:"is_hot"`
	IsAvoid         bool                      `json:"is_avoid"`
	IsConsolidating bool                      `json:"is_consolidating"`
	Regulation      *strategy.RegulationStatus `json:"regulation,omitempty"`
	Reasons         []string                  `json:"reasons"`
}

type scannerResponse struct {
	Stocks  []scannerRow           `json:"stocks"`
	Sectors []strategy.SectorScore `json:"sectors"`
}

// ListScanner returns swing-trading analysis (1-4 week horizon) for all watchlist stocks,
// sorted by score descending. Also returns sector rankings.
func (h *Handler) ListScanner(w http.ResponseWriter, r *http.Request) {
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

	// Pass 1: compute 40-day returns for RS benchmark
	var retList []float64
	retMap := make(map[string]float64)
	for _, s := range h.cfg.Watchlist {
		c := h.getCandles(s.Code)
		ret := strategy.PriceChangePct(c, 40)
		retMap[s.Code] = ret
		if ret != 0 {
			retList = append(retList, ret)
		}
	}
	var benchmark float64
	if len(retList) > 0 {
		var sum float64
		for _, v := range retList {
			sum += v
		}
		benchmark = sum / float64(len(retList))
	}

	// Pass 2: scanner analysis + limit analysis
	rows := make([]scannerRow, 0, len(h.cfg.Watchlist))
	scoreMap := make(map[string]int)

	for _, s := range h.cfg.Watchlist {
		quote, ok := snap[s.Code]
		if !ok {
			continue
		}
		candles := h.getCandles(s.Code)

		// Build regulation status from YAML config
		reg := strategy.ParseRegulation(s.Warn, s.WarnStart, s.WarnEnd, time.Now())

		// Taiwan limit analysis
		limit := strategy.AnalyzeLimits(quote, candles, reg)

		sig := strategy.ScannerAnalyze(quote, candles, cfg, benchmark, &limit)
		scoreMap[s.Code] = sig.Score

		rows = append(rows, scannerRow{
			Code:            s.Code,
			Name:            s.Name,
			Price:           quote.Price,
			Change:          quote.Change,
			ChangePct:       quote.ChangePct,
			Score:           sig.Score,
			Action:          sig.Action,
			Stage:           sig.Stage,
			StageZh:         sig.StageZh,
			Holding:         sig.Holding,
			Risk:            sig.Risk,
			RiskZh:          sig.RiskZh,
			Sector:          sig.Sector,
			RS:              sig.RS,
			Is60DayHigh:     sig.Is60DayHigh,
			Is120DayHigh:    sig.Is120DayHigh,
			MA20:            sig.MA20,
			MA60:            sig.MA60,
			MA120:           sig.MA120,
			LimitStatus:     sig.LimitStatus,
			LimitStatusZh:   sig.LimitStatusZh,
			LimitUpDays5:    sig.LimitUpDays5,
			LimitDownDays5:  sig.LimitDownDays5,
			OpenLimitType:   sig.OpenLimitType,
			IsHot:           sig.IsHot,
			IsAvoid:         sig.IsAvoid,
			IsConsolidating: sig.IsConsolidating,
			Regulation:      sig.Regulation,
			Reasons:         sig.Reasons,
		})
	}

	// Compute sector rankings and enrich rows
	sectors := strategy.RankSectors(scoreMap)
	for i := range rows {
		sector := rows[i].Sector
		rows[i].SectorRank = strategy.SectorRank(sector, sectors)
		rows[i].SectorFlow = strategy.SectorFlow(sector, sectors)
		if rows[i].SectorFlow == "流入" && sector != "其他" {
			rows[i].Reasons = append(rows[i].Reasons, sector+"族群資金流入")
		}
	}

	// Sort by score descending
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Score > rows[j].Score
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scannerResponse{
		Stocks:  rows,
		Sectors: sectors,
	})
}

// ── Radar (intraday day-trading) ───────────────────────────────────────────────

// ListRadar serves multi-timeframe (3m/5m/15m) intraday analysis plus order-flow
// trend for every watchlist stock, sorted by score descending.
func (h *Handler) ListRadar(w http.ResponseWriter, r *http.Request) {
	codes := h.codes()

	switch {
	case h.cache.IsEmpty():
		h.cache.Refresh(codes, h.realtime)
	case h.cache.IsStale():
		h.cache.RefreshAsync(codes, h.realtime)
	}

	snap, _ := h.cache.Snapshot()
	cfg := radar.Config{
		BuyScore:   h.cfg.Strategy.BuyScore,
		WatchScore: h.cfg.Strategy.WatchScore,
	}

	now := time.Now()
	out := make([]radar.RadarSignal, 0, len(h.cfg.Watchlist))
	for _, s := range h.cfg.Watchlist {
		quote, ok := snap[s.Code]
		if !ok {
			continue
		}

		set, ok := h.getIntraday(s.Code, quote)
		if !ok {
			continue
		}

		// Record this on-demand sample too, then read the full window.
		h.flow.Record(s.Code, quote.BidVol, quote.AskVol, now)
		flow := orderflow.Analyze(h.flow.Series(s.Code))

		sig := radar.Analyze(quote, set, flow, cfg)
		if s.Name != "" {
			sig.Name = s.Name
		}
		out = append(out, sig)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
