package cache

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"datahunter/internal/bitunix"
)

// bitunixAccount is one Bitunix account to mirror opens onto. The exit is managed
// by the exchange via the TP/SL attached at entry (so reversed/expired paper-
// closes are NOT mirrored — the real position rides to its TP or SL).
type bitunixAccount struct {
	label  string // "帳1" / "帳2" for logs
	cli    *bitunix.Client
	pct    float64
	lev    int             // 0 = use each coin's max leverage
	margin float64         // fixed margin per order (USDT); >0 overrides pct
	books  map[string]bool // book names to mirror; "all" → every book
}

func (a *bitunixAccount) wants(book string) bool { return a.books["all"] || a.books[book] }

// bitunixTrader fans a strategy open out to one or more accounts.
type bitunixTrader struct{ accts []*bitunixAccount }

// newBitunixTrader builds the trader from env, or nil if disabled/unconfigured.
// Account 1 uses the unprefixed vars; accounts 2..9 use a BITUNIX_<n>_ prefix.
//
//	BITUNIX_AUTOTRADE=1            (master switch; default off — gates ALL accounts)
//	BITUNIX_API_KEY / BITUNIX_API_SECRET
//	BITUNIX_RISK_PCT=1            (margin as % of available; default 1)
//	BITUNIX_MARGIN_USDT=1.5      (fixed margin/order; >0 overrides RISK_PCT)
//	BITUNIX_LEVERAGE=25          (default 25; 0 or "max" = each coin's max)
//	BITUNIX_BOOKS=emaonly        (all | comma list of main,gamble,emaonly)
//
//	# second account (optional): same keys with a 2_ prefix
//	BITUNIX_2_API_KEY / BITUNIX_2_API_SECRET / BITUNIX_2_BOOKS / BITUNIX_2_MARGIN_USDT / ...
func newBitunixTrader() *bitunixTrader {
	if os.Getenv("BITUNIX_AUTOTRADE") != "1" {
		return nil
	}
	var accts []*bitunixAccount
	for i := 1; i <= 9; i++ {
		prefix := "BITUNIX_"
		if i > 1 {
			prefix = fmt.Sprintf("BITUNIX_%d_", i)
		}
		key, secret := os.Getenv(prefix+"API_KEY"), os.Getenv(prefix+"API_SECRET")
		if key == "" || secret == "" {
			continue // no keys for this slot → skip
		}
		if a := buildAccount(fmt.Sprintf("帳%d", i), prefix, key, secret); a != nil {
			accts = append(accts, a)
		}
	}
	if len(accts) == 0 {
		log.Printf("bitunix autotrade: BITUNIX_AUTOTRADE=1 but no account has API keys — disabled")
		return nil
	}
	return &bitunixTrader{accts: accts}
}

func buildAccount(label, prefix, key, secret string) *bitunixAccount {
	pct := 1.0
	if v, err := strconv.ParseFloat(os.Getenv(prefix+"RISK_PCT"), 64); err == nil && v > 0 {
		pct = v
	}
	lev := 25 // 0 = each coin's max leverage
	switch v := strings.TrimSpace(os.Getenv(prefix + "LEVERAGE")); {
	case v == "":
	case strings.EqualFold(v, "max") || v == "0":
		lev = 0
	default:
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lev = n
		}
	}
	var margin float64
	if v, err := strconv.ParseFloat(os.Getenv(prefix+"MARGIN_USDT"), 64); err == nil && v > 0 {
		margin = v
	}
	books := map[string]bool{}
	raw := strings.TrimSpace(os.Getenv(prefix + "BOOKS"))
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
	levStr := fmt.Sprintf("%dx", lev)
	if lev == 0 {
		levStr = "該幣最大"
	}
	sizeStr := fmt.Sprintf("risk %.2f%%", pct)
	if margin > 0 {
		sizeStr = fmt.Sprintf("固定保證金 %.4fU/單", margin)
	}
	log.Printf("bitunix autotrade: %s ENABLED (%s, lev %s, books=%s)", label, sizeStr, levStr, raw)
	return &bitunixAccount{label: label, cli: bitunix.New(key, secret), pct: pct, lev: lev, margin: margin, books: books}
}

// mirrorOpen fires a real Bitunix order per matching account. Async and fully
// isolated: any failure is logged and never affects the paper engine.
func (t *bitunixTrader) mirrorOpen(book, coin, dir string, entry, tp, sl float64) {
	for _, a := range t.accts {
		if !a.wants(book) {
			continue
		}
		a := a
		go func() {
			res, err := a.cli.Open(coin+"USDT", dir, a.pct, a.lev, entry, tp, sl, "USDT", a.margin)
			if err != nil {
				log.Printf("bitunix autotrade: %s [%s] %s %s FAILED: %v", a.label, book, coin, dir, err)
				return
			}
			log.Printf("bitunix autotrade: %s [%s] %s %s OK — qty %s · %dx · 保證金 %.2fU · 名目 %.2fU @ %.6g (TP %.6g / SL %.6g)",
				a.label, book, coin, dir, res.Qty, res.Lev, res.Margin, res.Notional, res.Price, tp, sl)
		}()
	}
}
