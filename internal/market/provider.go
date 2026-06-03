package market

// RealtimeProvider fetches current quote data.
type RealtimeProvider interface {
	GetQuote(code string) (*Quote, error)
}

// HistoryProvider fetches historical daily candle data.
// market is "TW" (TSE/上市) or "TWO" (TPEX/上櫃).
// Pass an empty string to trigger auto-detection with result caching.
type HistoryProvider interface {
	GetCandles(code, market string) ([]Candle, error)
}
