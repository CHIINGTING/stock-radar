package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RefreshSeconds int `yaml:"refresh_seconds"`
	Strategy       struct {
		BuyScore   int `yaml:"buy_score"`
		WatchScore int `yaml:"watch_score"`
	} `yaml:"strategy"`
	Positions []PositionConfig `yaml:"positions"`
	Watchlist []StockConfig    `yaml:"watchlist"`
}

type StockConfig struct {
	Code      string `yaml:"code"       json:"code"`
	Name      string `yaml:"name"       json:"name"`
	Market    string `yaml:"market"     json:"market"`     // "TW" (上市) or "TWO" (上櫃); empty = auto-detect
	Warn      string `yaml:"warn"       json:"warn"`       // "處置股" / "注意股"
	WarnStart string `yaml:"warn_start" json:"warn_start"` // YYYY-MM-DD
	WarnEnd   string `yaml:"warn_end"   json:"warn_end"`   // YYYY-MM-DD
}

type PositionConfig struct {
	Code   string  `yaml:"code"   json:"code"`
	Name   string  `yaml:"name"   json:"name"`
	Entry  float64 `yaml:"entry"  json:"entry"`
	Shares int     `yaml:"shares" json:"shares"`
	Market string  `yaml:"market" json:"market"` // "TW" (上市) or "TWO" (上櫃); empty = auto-detect
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}

	if cfg.RefreshSeconds == 0 {
		cfg.RefreshSeconds = 3
	}
	if cfg.Strategy.BuyScore == 0 {
		cfg.Strategy.BuyScore = 80
	}
	if cfg.Strategy.WatchScore == 0 {
		cfg.Strategy.WatchScore = 60
	}

	return &cfg, nil
}
