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
	TWSE      TWSEConfig       `yaml:"twse"`
	Yahoo     YahooConfig      `yaml:"yahoo"`
	Positions []PositionConfig `yaml:"positions"`
	Watchlist []StockConfig    `yaml:"watchlist"`
}

// YahooConfig tunes the Yahoo Finance client (daily history + intraday K). HTTP/2
// stays on by default (Yahoo serves it fine); set force_http1 if your network
// also resets HTTP/2 to Yahoo.
type YahooConfig struct {
	ForceHTTP1       *bool `yaml:"force_http1"`       // disable HTTP/2 (default false)
	TimeoutSeconds   int   `yaml:"timeout_seconds"`   // per-request timeout (default 10)
	DisableKeepAlive bool  `yaml:"disable_keepalive"` // close connection after each request
}

// TWSEConfig tunes the realtime MIS client. All fields are optional; unset values
// keep the hardened defaults. force_http1 defaults to true because the TWSE MIS
// endpoint resets HTTP/2 connections (confirmed via cmd/twse-diag).
type TWSEConfig struct {
	MaxConcurrent    int   `yaml:"max_concurrent"`    // global in-flight cap (default 3)
	TimeoutSeconds   int   `yaml:"timeout_seconds"`   // per-request timeout (default 10)
	MaxRetries       int   `yaml:"max_retries"`       // retries on transient errors (default 3)
	ForceHTTP1       *bool `yaml:"force_http1"`       // disable HTTP/2 (default true)
	DisableKeepAlive bool  `yaml:"disable_keepalive"` // close connection after each request
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
