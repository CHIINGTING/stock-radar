package market

type Quote struct {
	Code string
	Name string

	Price float64

	Open float64
	High float64
	Low  float64

	Volume    int64
	RefPrice  float64
	Change    float64
	ChangePct float64

	BidVol int64 // sum of top-5 bid queue volume
	AskVol int64 // sum of top-5 ask queue volume
}
