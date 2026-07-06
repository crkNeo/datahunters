package cache

import (
	"log"
	"os"
	"strconv"
	"strings"

	"datahunter/internal/bitunix"
)

// bitunixTrader mirrors strategy OPENS onto a real Bitunix account. Phase 1:
// admin's own keys from env; the exit is managed by the exchange via the TP/SL
// attached at entry (so reversed/expired paper-closes are NOT mirrored — the
// real position rides to its TP or SL). The EMA book (銀河) is the cleanest fit
// since it only ever closes on TP/SL.
type bitunixTrader struct {
	cli   *bitunix.Client
	pct   float64
	lev   int
	books map[string]bool // book names to mirror; "all" → every book
}

// newBitunixTrader builds the trader from env, or nil if disabled/unconfigured:
//
//	BITUNIX_AUTOTRADE=1            (master switch; default off)
//	BITUNIX_API_KEY / BITUNIX_API_SECRET
//	BITUNIX_RISK_PCT=1            (margin as % of available; default 1)
//	BITUNIX_LEVERAGE=25          (default 25)
//	BITUNIX_BOOKS=emaonly        (all | comma list of main,gamble,emaonly)
func newBitunixTrader() *bitunixTrader {
	if os.Getenv("BITUNIX_AUTOTRADE") != "1" {
		return nil
	}
	key, secret := os.Getenv("BITUNIX_API_KEY"), os.Getenv("BITUNIX_API_SECRET")
	if key == "" || secret == "" {
		log.Printf("bitunix autotrade: BITUNIX_AUTOTRADE=1 but API keys missing — disabled")
		return nil
	}
	pct := 1.0
	if v, err := strconv.ParseFloat(os.Getenv("BITUNIX_RISK_PCT"), 64); err == nil && v > 0 {
		pct = v
	}
	lev := 25
	if v, err := strconv.Atoi(os.Getenv("BITUNIX_LEVERAGE")); err == nil && v > 0 {
		lev = v
	}
	books := map[string]bool{}
	raw := strings.TrimSpace(os.Getenv("BITUNIX_BOOKS"))
	if raw == "" || strings.EqualFold(raw, "all") {
		books["all"] = true
		raw = "all"
	} else {
		for _, b := range strings.Split(raw, ",") {
			if b = strings.TrimSpace(b); b != "" {
				books[b] = true
			}
		}
	}
	log.Printf("bitunix autotrade: ENABLED (risk %.2f%%, lev %dx, books=%s)", pct, lev, raw)
	return &bitunixTrader{cli: bitunix.New(key, secret), pct: pct, lev: lev, books: books}
}

func (t *bitunixTrader) wants(book string) bool { return t.books["all"] || t.books[book] }

// mirrorOpen fires a real Bitunix order for a strategy open. Async and fully
// isolated: any failure is logged and never affects the paper engine.
func (t *bitunixTrader) mirrorOpen(book, coin, dir string, tp, sl float64) {
	if !t.wants(book) {
		return
	}
	go func() {
		res, err := t.cli.Open(coin+"USDT", dir, t.pct, t.lev, tp, sl, "USDT")
		if err != nil {
			log.Printf("bitunix autotrade: [%s] %s %s FAILED: %v", book, coin, dir, err)
			return
		}
		log.Printf("bitunix autotrade: [%s] %s %s OK — qty %s · 保證金 %.2fU · 名目 %.2fU @ %.6g (TP %.6g / SL %.6g)",
			book, coin, dir, res.Qty, res.Margin, res.Notional, res.Price, tp, sl)
	}()
}
