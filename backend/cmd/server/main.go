package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "time/tzdata" // embed tz database so America/New_York works on Windows

	"datahunter/internal/api"
	"datahunter/internal/cache"
)

// loadDotEnv reads KEY=VALUE lines from a .env file (if present) into the
// environment, without overriding variables already set. No external deps.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

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
	loadDotEnv(".env")

	coins := defaultCoins
	if env := os.Getenv("COINS"); env != "" {
		coins = strings.Split(env, ",")
	}

	store := cache.NewStore(coins)

	// auth: seed the super-admin from env, resolve the token-signing secret
	store.SeedAdmin(os.Getenv("ADMIN_USER"), os.Getenv("ADMIN_PASS"))
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		secret = hex.EncodeToString(b)
		log.Printf("JWT_SECRET not set — using a random one (logins reset on restart; set JWT_SECRET in .env)")
	}

	// initial fill, then refresh on a ticker
	go func() {
		log.Printf("priming cache for %d coins...", len(coins))
		store.Refresh()
		log.Printf("initial cache ready")
		ticker := time.NewTicker(120 * time.Second) // lowered from 60s to cut REST load
		for range ticker.C {
			store.Refresh()
			log.Printf("cache refreshed")
		}
	}()

	// keep the breakout radar warm so /api/radar responds instantly
	go func() {
		store.Radar()
		log.Printf("breakout radar ready")
		ticker := time.NewTicker(180 * time.Second) // lowered from 90s to cut REST load
		for range ticker.C {
			store.Radar()
		}
	}()

	// paper-trading tracker: open/monitor/close simulated radar signals
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			store.PaperTick()
		}
	}()

	// US/macro risk backdrop (Yahoo), kept warm
	go func() {
		store.Risk()
		ticker := time.NewTicker(60 * time.Second)
		for range ticker.C {
			store.Risk()
		}
	}()

	// (order-book wall board removed: its per-coin Binance depth polling was the
	// biggest steady API-weight consumer and unrelated to the paper strategy.)

	// liquidation feed (OKX), polled + accumulated
	go func() {
		store.LiqTick()
		ticker := time.NewTicker(2 * time.Minute)
		for range ticker.C {
			store.LiqTick()
		}
	}()

	// Upbit announcement watcher → Telegram (no-op unless Telegram is configured)
	go func() {
		store.UpbitTick() // first call seeds the baseline (no history replay)
		ticker := time.NewTicker(20 * time.Second)
		for range ticker.C {
			store.UpbitTick()
		}
	}()

	srv := api.NewServer(store, secret)
	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}
