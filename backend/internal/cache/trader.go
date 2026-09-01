package cache

import (
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	"datahunter/internal/bitunix"
)

// bitunixAccount is one Bitunix account to mirror opens onto.
//
// exitMode 決定平倉行為:
//   - "single"(預設):開倉即掛「最終 TP + 初始 SL」一個 bracket,交易所自己跑到 TP/SL;
//     紙上策略的分批/套保/反轉/逾時都不鏡像。
//   - "follow":開倉只掛初始 SL,分批止盈 + 沿路套保 + 最終平倉全部跟著紙上策略事件走
//     (見 onLeg / onClose)。
type bitunixAccount struct {
	label    string // "帳1" / "帳2" for logs
	cli      *bitunix.Client
	pct      float64
	lev      int             // 0 = use each coin's max leverage
	margin   float64         // fixed margin per order (USDT); >0 overrides pct
	books    map[string]bool // book names to mirror; "all" → every book
	exitMode string          // single | follow
}

// followPos 追蹤一張「完全跟隨」真倉,對應一筆紙上策略單(每帳號一列)。持久化到 DB,
// 讓伺服器重啟後能接續管理。
type followPos struct {
	TradeID   string  // 紙上策略單 ID(book|coin|dir|opentime)
	Acct      string  // 帳號 label(重啟後用它重新對應到 *bitunixAccount)
	Symbol    string  // 交易所合約(1000PEPEUSDT…)
	PosID     string  // Bitunix positionId(移動止損用)
	Dir       string  // long | short
	Factor    float64 // 策略價 × Factor = 交易所價
	OrigQty   float64 // 原始成交數量(交易所 base 單位)
	BasePrec  int     // 數量精度
	QuotePrec int     // 價格精度
	Hedge     bool    // 雙向持倉(平倉要帶 tradeSide=CLOSE)
	Filled    float64 // 已分批平掉的比例(冪等追蹤)
}

// observeOnlyBooks are observation strategies that must NOT be swept in by the
// "all" wildcard — they only go live if named explicitly in BITUNIX_BOOKS. Keeps
// a strategy we're still validating (脈衝星) from placing real orders by accident.
var observeOnlyBooks = map[string]bool{"pulsar": true}

func (a *bitunixAccount) wants(book string) bool {
	if a.books[book] { // 明確點名 → 一律生效(含 observe-only)
		return true
	}
	return a.books["all"] && !observeOnlyBooks[book]
}

// Bitunix 完全跟隨掛鉤:store 在有 follow 帳號時注入,指向 trader.onLeg/onClose;
// 未設(無 follow 帳號 / 未啟用實盤)→ 空操作。stepTP 每段成交呼叫 mirrorLeg,
// closeTrade 平倉呼叫 mirrorClose。這樣兩個掛鉤點就涵蓋所有 book 的所有出場路徑。
var (
	exitMirrorLeg   func(tr *PaperTrade, weight float64)
	exitMirrorClose func(tr *PaperTrade)
)

func mirrorLeg(tr *PaperTrade, weight float64) {
	if exitMirrorLeg != nil {
		exitMirrorLeg(tr, weight)
	}
}

func mirrorClose(tr *PaperTrade) {
	if exitMirrorClose != nil {
		exitMirrorClose(tr)
	}
}

// bitunixTrader fans a strategy open out to one or more accounts.
type bitunixTrader struct {
	accts   []*bitunixAccount
	db      *DB                   // 持久化 follow 追蹤表(重啟續管);開機後由 store 注入
	fmu     sync.Mutex            // guards follows
	follows map[string]*followPos // key: tradeID|acct
}

func fkey(tradeID, acct string) string { return tradeID + "\x00" + acct }

func (t *bitunixTrader) getFollow(tradeID, acct string) *followPos {
	t.fmu.Lock()
	defer t.fmu.Unlock()
	return t.follows[fkey(tradeID, acct)]
}

func (t *bitunixTrader) putFollow(fp *followPos) {
	t.fmu.Lock()
	t.follows[fkey(fp.TradeID, fp.Acct)] = fp
	t.fmu.Unlock()
	if t.db != nil {
		t.db.saveFollow(fp)
	}
}

func (t *bitunixTrader) addFilled(tradeID, acct string, w float64) {
	t.fmu.Lock()
	if fp := t.follows[fkey(tradeID, acct)]; fp != nil {
		fp.Filled += w
	}
	t.fmu.Unlock()
	if t.db != nil {
		t.db.updateFollowFilled(tradeID, acct, w)
	}
}

func (t *bitunixTrader) removeFollow(tradeID, acct string) {
	t.fmu.Lock()
	delete(t.follows, fkey(tradeID, acct))
	t.fmu.Unlock()
	if t.db != nil {
		t.db.deleteFollow(tradeID, acct)
	}
}

// hasFollow reports whether any account runs in follow mode (so the store only
// wires the stepTP/closeTrade hooks when needed).
func (t *bitunixTrader) hasFollow() bool {
	for _, a := range t.accts {
		if a.exitMode == "follow" {
			return true
		}
	}
	return false
}

// newBitunixTrader builds the trader from env, or nil if disabled/unconfigured.
// Account 1 uses the unprefixed vars; accounts 2..9 use a BITUNIX_<n>_ prefix.
//
//	BITUNIX_AUTOTRADE=1            (master switch; default off — gates ALL accounts)
//	BITUNIX_API_KEY / BITUNIX_API_SECRET
//	BITUNIX_RISK_PCT=1            (margin as % of available; default 1)
//	BITUNIX_MARGIN_USDT=1.5      (fixed margin/order; >0 overrides RISK_PCT)
//	BITUNIX_LEVERAGE=25          (default 25; 0 or "max" = each coin's max)
//	BITUNIX_BOOKS=emaonly        (all | comma list of main,gamble,emaonly)
//	BITUNIX_EXIT_MODE=single     (single = 最終TP+初始SL bracket;follow = 完全跟隨分批+套保)
//
//	# second account (optional): same keys with a 2_ prefix
//	BITUNIX_2_API_KEY / BITUNIX_2_API_SECRET / BITUNIX_2_BOOKS / BITUNIX_2_EXIT_MODE / ...
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
	t := &bitunixTrader{accts: accts, follows: map[string]*followPos{}}
	t.selfCheck() // 啟動時對每個帳號自檢:金鑰/連線 + 持倉模式 + 餘額(非阻塞)
	return t
}

// selfCheck 對每個帳號打一次 Account(),把「持倉模式(ONE_WAY/HEDGE)+ 可用餘額」寫進 log,
// 讓部署後一眼確認金鑰通不通、hedge 判斷對不對。非阻塞(各帳號各自 goroutine)。
func (t *bitunixTrader) selfCheck() {
	for _, a := range t.accts {
		a := a
		go func() {
			avail, mode, err := a.cli.Account("USDT")
			if err != nil {
				log.Printf("bitunix autotrade: %s 自檢失敗(金鑰/連線?): %v", a.label, err)
				return
			}
			books := "all"
			if !a.books["all"] {
				list := make([]string, 0, len(a.books))
				for b := range a.books {
					list = append(list, b)
				}
				books = strings.Join(list, ",")
			}
			log.Printf("bitunix autotrade: %s 自檢 OK — 持倉模式=%s · 可用餘額=%.2f USDT · exit=%s · books=%s",
				a.label, mode, avail, a.exitMode, books)
		}()
	}
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
	exitMode := "single"
	if strings.EqualFold(strings.TrimSpace(os.Getenv(prefix+"EXIT_MODE")), "follow") {
		exitMode = "follow"
	}
	levStr := fmt.Sprintf("%dx", lev)
	if lev == 0 {
		levStr = "該幣最大"
	}
	sizeStr := fmt.Sprintf("risk %.2f%%", pct)
	if margin > 0 {
		sizeStr = fmt.Sprintf("固定保證金 %.4fU/單", margin)
	}
	log.Printf("bitunix autotrade: %s ENABLED (%s, lev %s, books=%s, exit=%s)", label, sizeStr, levStr, raw, exitMode)
	return &bitunixAccount{label: label, cli: bitunix.New(key, secret), pct: pct, lev: lev, margin: margin, books: books, exitMode: exitMode}
}

// mirrorOpen fires a real Bitunix order per matching account. Async and fully
// isolated: any failure is logged and never affects the paper engine.
func (t *bitunixTrader) mirrorOpen(book string, tr *PaperTrade) {
	for _, a := range t.accts {
		if !a.wants(book) {
			continue
		}
		a := a
		if a.exitMode == "follow" {
			go t.followOpen(a, book, tr)
		} else {
			go t.singleOpen(a, book, tr)
		}
	}
}

// singleOpen(預設):最終 TP + 初始 SL 一個 bracket,掛完交給交易所。
func (t *bitunixTrader) singleOpen(a *bitunixAccount, book string, tr *PaperTrade) {
	res, err := a.cli.Open(tr.Coin+"USDT", tr.Dir, a.pct, a.lev, tr.Entry, tr.TP, tr.SL, "USDT", a.margin)
	if err != nil {
		log.Printf("bitunix autotrade: %s [%s] %s %s FAILED: %v", a.label, book, tr.Coin, tr.Dir, err)
		return
	}
	log.Printf("bitunix autotrade: %s [%s] %s %s OK — qty %s · %dx · 保證金 %.2fU · 名目 %.2fU @ %.6g (TP %.6g / SL %.6g)",
		a.label, book, tr.Coin, tr.Dir, res.Qty, res.Lev, res.Margin, res.Notional, res.Price, tr.TP, tr.SL)
}

// followOpen(完全跟隨):只掛初始 SL(不掛 TP,分批止盈由 onLeg 驅動),記下 positionId 與
// 原始數量以便後續分批平倉 / 移動止損,並持久化。
func (t *bitunixTrader) followOpen(a *bitunixAccount, book string, tr *PaperTrade) {
	res, err := a.cli.Open(tr.Coin+"USDT", tr.Dir, a.pct, a.lev, tr.Entry, 0, tr.SL, "USDT", a.margin)
	if err != nil {
		log.Printf("bitunix follow: %s [%s] %s %s 開倉 FAILED: %v", a.label, book, tr.Coin, tr.Dir, err)
		return
	}
	posID, perr := a.cli.PositionID(res.Symbol, tr.Dir)
	if perr != nil {
		// 沒抓到 positionId → 套保(移動止損)會失效,但初始 SL 已掛、分批平倉仍可用。
		log.Printf("bitunix follow: %s [%s] %s 抓 positionId 失敗(套保將失效,初始 SL 仍在): %v", a.label, book, tr.Coin, perr)
	}
	fp := &followPos{
		TradeID: tr.ID, Acct: a.label, Symbol: res.Symbol, PosID: posID, Dir: tr.Dir,
		Factor: res.Factor, OrigQty: res.QtyF, BasePrec: res.BasePrec, QuotePrec: res.QuotePrec,
		Hedge: res.PosMode == "HEDGE",
	}
	t.putFollow(fp)
	log.Printf("bitunix follow: %s [%s] %s %s 開倉 OK — qty %s · %dx · 名目 %.2fU @ %.6g (初始 SL %.6g, posID=%s)",
		a.label, book, tr.Coin, tr.Dir, res.Qty, res.Lev, res.Notional, res.Price, tr.SL, posID)
}

// onLeg 在紙上策略某段止盈成交(Legs 增加)後,對每個 follow 帳號:分批平掉 weight 比例 +
// 把交易所止損上移到策略當下的 tr.SL(沿路套保)。
func (t *bitunixTrader) onLeg(tr *PaperTrade, weight float64) {
	if weight <= 0 {
		return
	}
	for _, a := range t.accts {
		if a.exitMode != "follow" {
			continue
		}
		fp := t.getFollow(tr.ID, a.label)
		if fp == nil {
			continue
		}
		a, fp, sl := a, fp, tr.SL
		go func() {
			q := floorTo(fp.OrigQty*weight, fp.BasePrec)
			if err := a.cli.CloseQtyMarket(fp.Symbol, fp.Dir, q, fp.BasePrec, fp.Hedge, fp.PosID); err != nil {
				log.Printf("bitunix follow: %s [%s] 分批平倉 %.4f 失敗: %v", a.label, tr.ID, q, err)
			} else {
				log.Printf("bitunix follow: %s [%s] 分批平倉 %.4f(比例 %.0f%%)OK", a.label, tr.ID, q, weight*100)
			}
			if err := a.cli.SetPositionSL(fp.Symbol, fp.PosID, sl*fp.Factor, fp.QuotePrec); err != nil {
				log.Printf("bitunix follow: %s [%s] 移動止損→%.6g 失敗: %v", a.label, tr.ID, sl, err)
			}
		}()
		t.addFilled(tr.ID, a.label, weight)
	}
}

// onClose 在紙上策略平倉(最終 TP / 停損 / 手動 / 逾時 / 反轉,任何原因)後,對每個 follow
// 帳號:市價平掉剩餘(送原始數量 reduce-only,交易所只會平掉還剩的),並移除追蹤。
func (t *bitunixTrader) onClose(tr *PaperTrade) {
	for _, a := range t.accts {
		if a.exitMode != "follow" {
			continue
		}
		fp := t.getFollow(tr.ID, a.label)
		if fp == nil {
			continue
		}
		a, fp := a, fp
		go func() {
			// 最終平倉:依 positionId 整倉市價平(只動這一倉,分倉/避險安全)。沒有 positionId
			// 才退回用原始數量 reduce-only(交易所會平掉還剩的)。
			var err error
			if fp.PosID != "" {
				err = a.cli.FlashClose(fp.PosID)
			} else {
				err = a.cli.CloseQtyMarket(fp.Symbol, fp.Dir, fp.OrigQty, fp.BasePrec, fp.Hedge, "")
			}
			if err != nil {
				log.Printf("bitunix follow: %s [%s] 最終平倉失敗: %v", a.label, tr.ID, err)
			} else {
				log.Printf("bitunix follow: %s [%s] 最終平倉 OK(%s)", a.label, tr.ID, tr.Outcome)
			}
		}()
		t.removeFollow(tr.ID, a.label)
	}
}

// floorTo rounds x DOWN to n decimals (mirror of the bitunix pkg helper; never over-sizes).
func floorTo(x float64, n int) float64 { f := math.Pow10(n); return math.Floor(x*f) / f }
