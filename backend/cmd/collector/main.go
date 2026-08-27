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
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"datahunter/internal/collector"
	"datahunter/internal/exchange"
	"datahunter/internal/unlock"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	loadDotEnv(".env")

	cfg := collector.DefaultConfig()
	lcfg := collector.DefaultLabelConfig()

	dsn := flag.String("dsn", mysqlDSN(), "MySQL DSN (or set MYSQL_DSN / DB_HOST,DB_USER,…)")
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
	noUnlocks := flag.Bool("no-unlocks", false, "skip the daily token-unlock schedule capture")
	analyze := flag.Bool("analyze", false, "print the research report and exit (does not collect)")
	analyzeDays := flag.Int("analyze-days", 0, "limit -analyze to the last N days (0 = all history)")
	detail := flag.Int("event-detail", 0, "-analyze: 改為逐分鐘回放最大的 N 次事件(0 = 印統計報表)")
	detailBefore := flag.Int("detail-before", 30, "-event-detail: 回放事件前幾分鐘")
	detailAfter := flag.Int("detail-after", 10, "-event-detail: 回放事件後幾分鐘")
	oosFrom := flag.String("oos-from", "", "-pattern-backtest: 樣本外起始日 (YYYY-MM-DD),之前的資料視為設計期")
	patBT := flag.Bool("pattern-backtest", false, "-analyze: 把型態 A/B 的偵測條件跑過全部歷史,看觸發數與涵蓋率")
	lev := flag.Float64("leverage", 1, "-analyze: 以幾倍槓桿換算保證金報酬與強平距離(1 = 只看價格)")
	side := flag.String("side", "up", "-analyze: which tail to study — up (暴漲) or down (暴跌)")
	visPct := flag.Float64("visible-pct", 0, "-analyze: move already made before entry is assumed possible (0 = scale from -event-pct)")
	flag.Parse()

	cfg.SettleDelay = *settle
	lcfg.EventPct = *evPct

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("no MySQL DSN: pass -dsn, or set MYSQL_DSN (or DB_HOST/DB_USER/DB_PASS/DB_NAME) in backend/.env")
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

	// -analyze is a read-only report over what has already been collected. It
	// runs before anything else is started so it never competes with a live
	// collector for the connection pool, and it exits rather than falling
	// through into the loops.
	if *analyze {
		acfg := collector.DefaultAnalyzeConfig()
		acfg.Days = *analyzeDays
		acfg.EventPct = *evPct
		acfg.VisiblePct = *visPct
		acfg.Side = *side
		acfg.Leverage = *lev
		if *oosFrom != "" {
			t, err := time.Parse("2006-01-02", *oosFrom)
			if err != nil {
				log.Fatalf("-oos-from: %v (格式應為 YYYY-MM-DD)", err)
			}
			acfg.OOSFrom = t.UnixMilli()
		}
		if *patBT {
			if err := collector.PatternBacktest(db, acfg, os.Stdout); err != nil {
				log.Fatalf("pattern-backtest: %v", err)
			}
			return
		}
		if *detail > 0 {
			if err := collector.RunEventDetail(db, acfg, os.Stdout, *detail, *detailBefore, *detailAfter); err != nil {
				log.Fatalf("event-detail: %v", err)
			}
			return
		}
		if err := collector.RunAnalysis(db, acfg, os.Stdout); err != nil {
			log.Fatalf("analyze: %v", err)
		}
		return
	}

	ex := exchange.NewClient()
	c := collector.New(ex, db, cfg)
	if !*noUnlocks && !*labelOnly {
		c.EnableUnlocks(unlock.NewWatcher())
	}
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
		collector.LogPatternThresholds()
		go c.Run(stop)
		if !*noUnlocks {
			// Sparse, slow-moving and on entirely different hosts — its own
			// goroutine so a 40s dataset fetch can never delay a snapshot tick.
			go c.RunUnlocks(stop)
		}
	}
	if !*labelOnly {
		// Outcomes are backfilled on their own clock, well after detection, so a
		// result can never feed back into whether the signal was recorded.
		go c.RunPatternOutcomes(5*time.Minute, stop)
	}
	if !*noLabel {
		log.Printf("labeler: event threshold %.1f%% within 5m", lcfg.EventPct*100)
		go collector.RunLabeler(db, lcfg, 5*time.Minute, stop)
	}

	<-stop
	// Give an in-flight minute a moment to finish its writes before the pool closes.
	time.Sleep(2 * time.Second)
}

// mysqlDSN mirrors cache.mysqlDSN so the collector connects with whatever the
// site is already configured with — either MYSQL_DSN outright, or the
// DB_HOST / DB_PORT / DB_USER / DB_PASS / DB_NAME pieces. Returning "" when
// nothing is set lets main report a clear error instead of dialling localhost
// with a guessed password.
func mysqlDSN() string {
	if v := os.Getenv("MYSQL_DSN"); v != "" {
		return v
	}
	if os.Getenv("DB_HOST") == "" && os.Getenv("DB_USER") == "" && os.Getenv("DB_NAME") == "" {
		return ""
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	// epochs are stored as BIGINT so parseTime is unnecessary; force UTC + utf8mb4.
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=false&loc=UTC",
		get("DB_USER", "root"), os.Getenv("DB_PASS"),
		get("DB_HOST", "127.0.0.1"), get("DB_PORT", "3306"),
		get("DB_NAME", "datahunter"))
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
