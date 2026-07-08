package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "time/tzdata" // embed tz database so America/New_York works on Windows

	"golang.org/x/crypto/acme/autocert"

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

	// 支撐壓力 monitor (VIP): BTC/ETH/SOL/BNB support & resistance, breach alerts per
	// closed 1h bar. Runs off the in-memory WS klines (no REST); first tick seeds only.
	go func() {
		store.SRTick()
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			store.SRTick()
		}
	}()

	srv := api.NewServer(store, secret)

	// one process serves everything: the frontend SPA plus /api and /uploads.
	// STATIC_DIR overrides; otherwise auto-detect so it works whether you run from
	// backend/, cmd/server/, or the repo root.
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		for _, c := range []string{"../frontend/dist", "../../../frontend/dist", "frontend/dist", "dist"} {
			if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
				staticDir = c
				break
			}
		}
		if staticDir == "" {
			staticDir = "../frontend/dist"
		}
	}
	handler := withStatic(srv.Routes(), staticDir)

	// DOMAINS set → terminate HTTPS ourselves with Let's Encrypt (autocert):
	// :443 serves the app, :80 answers ACME challenges and redirects to https.
	// Needs root (or setcap) to bind 80/443 — you already run with sudo.
	if doms := splitTrim(os.Getenv("DOMAINS")); len(doms) > 0 {
		certDir := os.Getenv("CERT_DIR")
		if certDir == "" {
			certDir = "certs" // cached certs survive restarts (avoid LE rate limits)
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(doms...),
			Cache:      autocert.DirCache(certDir),
		}
		// :80 only answers ACME challenges + redirects — tight timeouts (slowloris)
		go func() {
			redirect := &http.Server{
				Addr: ":80", Handler: m.HTTPHandler(nil),
				ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
				WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
			}
			log.Fatal(redirect.ListenAndServe())
		}()
		s := &http.Server{
			Addr:              ":443",
			Handler:           handler,
			TLSConfig:         m.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second, // allows slow 3MB uploads on mobile
			IdleTimeout:       120 * time.Second,
		}
		log.Printf("serving HTTPS for %v (certs cached in %q, static from %q)", doms, certDir, staticDir)
		log.Fatal(s.ListenAndServeTLS("", ""))
	}

	// no DOMAINS → plain HTTP (local dev, or behind an external proxy)
	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("listening on %s (static from %q)", addr, staticDir)
	dev := &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	log.Fatal(dev.ListenAndServe())
}

// withStatic serves the SPA from dir, delegating API paths to the api handler
// and falling back to index.html for client-side routes (Vue Router history mode).
func withStatic(apiHandler http.Handler, dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/uploads/") || p == "/healthz" {
			apiHandler.ServeHTTP(w, r)
			return
		}
		// real file (asset) → serve it; anything else → SPA index.html
		if p != "/" {
			if st, err := os.Stat(filepath.Join(dir, filepath.Clean("/"+p))); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	})
}

// splitTrim splits a comma list and drops blanks (e.g. DOMAINS).
func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
