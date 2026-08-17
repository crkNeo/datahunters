// Command collector records a per-minute snapshot of the Binance USDT-perp
// market into MySQL, and backfills what happened after each snapshot.
//
// It is a research recorder, not a trading tool. It stores raw observations
// only — no scores, no thresholds, no signals — so that scoring rules can be
// written, rewritten and replayed offline against a fixed history rather than
// being frozen into the collection step.
//
// Run it alongside the dashboard server; it shares the database but not the
// process, so a crash or a rate-limit pause here cannot take the site down.
//
//	MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/datahunter?charset=utf8mb4&parseTime=false' \
//	  go run ./cmd/collector
//
// Flags override the defaults; -universe 100 tracks the 100 highest-turnover
// perps. Every tradable perp is still recorded daily in universe_1d regardless
// of the tracked slice, so a later analysis can see coins that were delisted.
package main

import (
	"bufio"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"datahunter/internal/collector"
	"datahunter/internal/exchange"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	loadDotEnv(".env")

	cfg := collector.DefaultConfig()
	lcfg := collector.DefaultLabelConfig()

	dsn := flag.String("dsn", os.Getenv("MYSQL_DSN"), "MySQL DSN (or set MYSQL_DSN)")
	flag.IntVar(&cfg.Universe, "universe", cfg.Universe, "symbols to track, ranked by 24h turnover")
	flag.IntVar(&cfg.Workers, "workers", cfg.Workers, "concurrent per-symbol fetches")
	flag.IntVar(&cfg.DepthEvery, "depth-every", cfg.DepthEvery, "minutes between order-book snapshots (0 disables)")
	flag.IntVar(&cfg.DepthLimit, "depth-limit", cfg.DepthLimit, "order-book levels per side")
	flag.IntVar(&cfg.SpotEvery, "spot-every", cfg.SpotEvery, "minutes between spot snapshots (0 disables)")
	flag.IntVar(&cfg.RetentionDays, "retention", cfg.RetentionDays, "days of history to keep (0 keeps all)")
	settle := flag.Duration("settle", cfg.SettleDelay, "delay past the minute boundary before reading a closed bar")
	evPct := flag.Float64("event-pct", lcfg.EventPct, "move within 5m that counts as an event, e.g. 0.10 for +10%")
	noLabel := flag.Bool("no-labeler", false, "collect only; skip forward-return backfill")
	labelOnly := flag.Bool("labeler-only", false, "backfill labels only; do not collect")
	flag.Parse()

	cfg.SettleDelay = *settle
	lcfg.EventPct = *evPct

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("no MySQL DSN: pass -dsn or set MYSQL_DSN")
	}
	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		log.Fatalf("open mysql: %v", err)
	}
	defer db.Close()
	// The dashboard server holds its own pool against the same database; keep
	// this one small so a burst of collector writes cannot starve the site of
	// connections.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err := db.Ping(); err != nil {
		log.Fatalf("ping mysql: %v", err)
	}

	ex := exchange.NewClient()
	c := collector.New(ex, db, cfg)
	if err := c.Init(); err != nil {
		log.Fatalf("init: %v", err)
	}

	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("collector: shutting down")
		close(stop)
	}()

	if !*labelOnly {
		log.Printf("collector: tracking %d symbols, depth every %dm (limit %d), spot every %dm, retention %dd",
			cfg.Universe, cfg.DepthEvery, cfg.DepthLimit, cfg.SpotEvery, cfg.RetentionDays)
		go c.Run(stop)
	}
	if !*noLabel {
		log.Printf("labeler: event threshold %.1f%% within 5m", lcfg.EventPct*100)
		go collector.RunLabeler(db, lcfg, 5*time.Minute, stop)
	}

	<-stop
	// Give an in-flight minute a moment to finish its writes before the pool closes.
	time.Sleep(2 * time.Second)
}

// loadDotEnv mirrors cmd/server: fill unset env vars from a .env file so the
// collector picks up the same MYSQL_DSN the site already uses.
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
		raw := strings.TrimSpace(line[eq+1:])
		if !strings.HasPrefix(raw, `"`) && !strings.HasPrefix(raw, `'`) {
			for i := 1; i < len(raw); i++ {
				if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
					raw = strings.TrimSpace(raw[:i])
					break
				}
			}
		}
		val := strings.Trim(raw, `"'`)
		if key != "" && os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
