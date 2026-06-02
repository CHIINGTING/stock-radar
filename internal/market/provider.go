package market

// RealtimeProvider fetches current quote data.
type RealtimeProvider interface {
	GetQuote(code string) (*Quote, error)
}

// HistoryProvider fetches historical daily candle data.
type HistoryProvider interface {
	GetCandles(code string) ([]Candle, error)
}
