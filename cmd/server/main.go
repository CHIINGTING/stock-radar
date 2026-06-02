package main

import (
	"log"
	"net/http"

	"stock-radar/internal/api"
	"stock-radar/internal/config"
)

func main() {

	cfg, err := config.Load(
		"stocks.yaml",
	)

	if err != nil {
		log.Fatal(err)
	}

	h := api.NewHandler(cfg)

	http.HandleFunc(
		"/api/stocks",
		h.ListStocks,
	)

	http.HandleFunc(
		"/api/analyze",
		h.AnalyzeStock,
	)

	http.HandleFunc(
		"/api/positions",
		h.ListPositions,
	)

	fs := http.FileServer(
		http.Dir("./web"),
	)

	http.Handle("/", fs)

	log.Println(
		"server running :8080",
	)

	log.Fatal(
		http.ListenAndServe(":8080", nil),
	)
}
