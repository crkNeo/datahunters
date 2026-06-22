package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"datahunter/internal/api"
	"datahunter/internal/cache"
)

// default coin universe; override with COINS env var (comma-separated).
// Kept to liquid OKX+Binance perps so every coin actually scores.
var defaultCoins = []string{
	// Layer 1
	"BTC", "ETH", "SOL", "BNB", "XRP", "ADA", "AVAX", "SUI", "LTC",
	"DOT", "TRX", "NEAR", "APT", "ATOM", "TON", "ICP", "FIL", "SEI", "TIA", "BCH",
	// Layer 2
	"ARB", "OP",
	// DeFi
	"LINK", "UNI", "AAVE", "ENA", "JUP", "INJ",
	// Meme
	"DOGE", "SHIB", "PEPE", "WIF", "TRUMP",
	// AI / infra
	"WLD", "FET", "ORDI",
}

func main() {
	coins := defaultCoins
	if env := os.Getenv("COINS"); env != "" {
		coins = strings.Split(env, ",")
	}

	store := cache.NewStore(coins)

	// initial fill, then refresh on a ticker
	go func() {
		log.Printf("priming cache for %d coins...", len(coins))
		store.Refresh()
		log.Printf("initial cache ready")
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			store.Refresh()
			log.Printf("cache refreshed")
		}
	}()

	srv := api.NewServer(store)
	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}
