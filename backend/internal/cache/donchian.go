package cache

import (
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// donchian.go — 唐奇安長通道突破(1h,多空都做)。規則照回測程式碼:
//
//	進場  這根 1H 收盤 > 前 168 根最高價(不含這根)→ 下一根開盤做多;< 前 168 根最低價 → 做空
//	      只看收盤;同一個幣同時只持有一個方向,不加碼
//	出場  先碰到哪個用哪個:
//	      ① 通道出場:多單收盤 < 前 12 根最低價(空單 > 前 12 根最高價)→ 下一根開盤平倉
//	      ② 初始停損(固定不移動):進場價 ∓ 3 × ATR(24),ATR 是訊號那根往前 24 根真實波幅的平均;
//	         盤中觸價就出場,跳空越過停損價以開盤價成交
//	      沒有固定止盈。平倉的同一根如果剛好反向突破,可以馬上反手。
//
// 另外記給「之後調整止盈止損」用的資料:最大獲利 + 它出現的時間、最大回撤(進場後最不利的位置)
// —— 用 K 線從進場那根算到出場那根,盤中再用即時價補;頁面換算成 R(= 進場到停損的距離)。
// 持倉另外顯示目前的 ATR(24) 和通道出場價(只放記憶體,每小時更新)。
//
// 唐奇安v2(donchianv2)= 同一套進場 / 初始停損 / 12 根通道出場,再多一條「吊燈停利 3 × ATR」,先到先出:
//
//	每根 1H 收盤:多單收盤 < 進場後最高收盤 − 3 × ATR(24) → 下一根開盤平倉(記 chandelier)
//	              空單收盤 > 進場後最低收盤 + 3 × ATR(24) → 下一根開盤平倉
//	只看收盤(不看盤中高低點);ATR(24) 每根重算、用那根收盤時的最新值;同一根也碰到通道出場就記 channel。
//	持倉超過 1h K 快取的範圍時(約 7 天),另外從進場前 25 根開始抓整段 1h K,進場後的最高 / 最低收盤才不會漏。
//
// 唐奇安v3(donchianv3)= 成交額前 30 的加密幣,1H:
//
//	進場  收盤突破前 336 根最高(或跌破最低),而且突破前的通道寬度((336 根最高 − 最低)÷ 收盤)
//	      低於過去 2000 根通道寬度的中位數(盤整夠窄才算),而且同方向 48 小時內沒被初始停損打掉過
//	      → 下一根開盤進場
//	停損  初始停損 3 × ATR(24)
//	出場  24 根通道出場 + 吊燈停利 4 × ATR(24),先到先出(規則同 v2,只是參數不同)
//	需要約 2340 根 1h K:每個幣開機時抓一次長歷史(每分鐘最多 8 個幣),之後每小時用共用的 1h K 快取接上。
//
// 實作:每小時收盤後處理一次(交易所新棒還沒生成的幣,下一分鐘再補)。持倉用 K 線從進場那根「重播」
// 停損 / 通道出場 —— 結果只由 K 線決定,重複跑也一樣,停機期間該出場的開機後照樣補出場;
// 新進場只看最新一根已收的 K,開機第一輪只管持倉、不進場。盤中另由 DonchianMarkTick 每 20 秒
// 用即時價檢查停損(掛停損單的效果)。觀察用,observeOnlyBooks 保證不會跟實單。
const (
	dcV3EntryN    = 336  // v3 進場通道:前 336 根(14 天)
	dcV3WidthN    = 2000 // v3 通道寬度的中位數:看過去 2000 根
	dcV3ExitN     = 24   // v3 出場通道:前 24 根
	dcV3ChandK    = 4.0  // v3 吊燈停利:4 × ATR
	dcV3StopCool  = 48 * dcBarMs
	dcV3TopN      = 30   // v3 幣池:成交額前 30
	dcV3Keep      = 2600 // v3 每個幣留幾根 1h 歷史
	dcV3LoadPerMn = 8    // v3 每分鐘最多抓幾個幣的長歷史
)

const (
	dcEntryN = 168 // 進場通道:前 168 根(7 天)
	dcExitN  = 12  // 出場通道:前 12 根(12 小時)
	dcATRN   = 24  // ATR 根數
	dcATRK   = 3.0 // 停損 = 進場 ∓ 3 × ATR
	dcBarMs  = 3600_000
	dcKlines = 200 // 168 + 24 + 餘裕;最後一根是還在走的(它的開盤 = 「下一根開盤」成交價)
)

func (s *Store) newDonchianBook() *microBook {
	b := &microBook{name: "donchian", tf: "1h", barSec: 3600, klimit: dcKlines, keep: 500, wideMark: true}
	b.universe = s.wideUniverse
	return b
}

func (s *Store) newDonchianV2Book() *microBook {
	b := s.newDonchianBook()
	b.name = "donchianv2"
	return b
}

func (s *Store) newDonchianV3Book() *microBook {
	b := s.newDonchianBook()
	b.name = "donchianv3"
	b.universe = s.dcV3Universe
	return b
}

// dcV3Universe = 寬幣池(已依成交額由大到小、只留加密幣)的前 30。
func (s *Store) dcV3Universe() []string {
	u := s.wideUniverse()
	if len(u) > dcV3TopN {
		u = u[:dcV3TopN]
	}
	return u
}

// dcRunner 是一本唐奇安書 + 它的每小時排程與出場參數:exitN = 出場通道根數;chandK = 吊燈停利幾倍 ATR(0 = 沒有);
// v3 = 用 v3 的進場(336 根 + 通道寬度 + 48h 停損冷卻)和長歷史。
type dcRunner struct {
	b      *microBook
	eng    *hourEngine
	exitN  int
	chandK float64
	v3     bool
}

func (s *Store) dcRunners() []dcRunner {
	var out []dcRunner
	if s.donchianBook != nil && s.dcEng != nil {
		out = append(out, dcRunner{b: s.donchianBook, eng: s.dcEng, exitN: dcExitN})
	}
	if s.donchianV2Book != nil && s.dcEng2 != nil {
		out = append(out, dcRunner{b: s.donchianV2Book, eng: s.dcEng2, exitN: dcExitN, chandK: dcATRK})
	}
	if s.donchianV3Book != nil && s.dcEng3 != nil {
		out = append(out, dcRunner{b: s.donchianV3Book, eng: s.dcEng3, exitN: dcV3ExitN, chandK: dcV3ChandK, v3: true})
	}
	return out
}

// wideUniverse = 成交額前 200(與 EMA200 監控 / 爆發雷達同一批,已排除穩定幣)扣掉代幣化股票 / 商品。
// 剛開機爆量脈搏還沒掃到行情時,先用銀河 top-80 頂著。
func (s *Store) wideUniverse() []string {
	base := s.e2Universe()
	if len(base) == 0 {
		base = s.emaCoins()
	}
	var out []string
	for _, c := range base {
		if s.cryptoOnly(c, nil) {
			out = append(out, c)
		}
	}
	return out
}

// hourEngine 是「每小時收盤後處理一次」的排程狀態(唐奇安 / 波動突破共用)。只在單一 ticker goroutine 用。
type hourEngine struct {
	bucket int64           // 處理到哪一個小時
	seeded bool            // 開機第一輪(只管持倉、不進場)跑過了沒
	boot   bool            // 這一小時是不是開機第一輪
	pend   map[string]bool // 這一小時還沒處理到的幣(交易所新棒還沒生成 → 下一分鐘再試)
}

// due 回傳這一分鐘要處理的幣;新的一小時重新排入整個宇宙 + 持倉幣。過了 10 分鐘還沒生成的就放棄這一小時。
func (e *hourEngine) due(now time.Time, coins func() []string) []string {
	bkt := now.Unix() / 3600
	if bkt != e.bucket {
		e.bucket = bkt
		e.boot = !e.seeded
		e.seeded = true
		e.pend = map[string]bool{}
		for _, c := range coins() {
			e.pend[c] = true
		}
	} else if now.Unix()-bkt*3600 > 600 {
		e.pend = nil
	}
	out := make([]string, 0, len(e.pend))
	for c := range e.pend {
		out = append(out, c)
	}
	return out
}

// hourBars 抓 1h K(走短 TTL 快取,唐奇安和波動突破同一小時共用),切成已收的 + 還在走的那根。
// ok=false:抓不到,或交易所還沒生成新的一根(剛收的那根還被當成「還在走」)→ 下一分鐘再試。
func (s *Store) hourBars(coin string, now time.Time) (closed []exchange.Candle, forming *exchange.Candle, ok bool) {
	v, err := s.candleCache.get(fmt.Sprintf("%s|1h|%d", coin, dcKlines), func() (any, error) {
		return s.ex.BinanceKlines(coin+"USDT", "1h", dcKlines)
	})
	if err != nil {
		return nil, nil, false
	}
	cs := v.([]exchange.Candle)
	if len(cs) < 2 {
		return nil, nil, false
	}
	cur := now.UnixMilli() / dcBarMs * dcBarMs
	last := cs[len(cs)-1]
	if last.Ts != cur {
		return nil, nil, false
	}
	return cs[:len(cs)-1], &last, true
}

func donchianCoins(b *microBook) []string {
	coins := b.universe()
	seen := map[string]bool{}
	for _, c := range coins {
		seen[c] = true
	}
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Status == "open" && !seen[tr.Coin] { // 掉出宇宙的持倉幣也要繼續管
			seen[tr.Coin] = true
			coins = append(coins, tr.Coin)
		}
	}
	b.mu.Unlock()
	return coins
}

// DonchianTick 每分鐘由 main 呼叫:整點收盤後處理出場、新進場(唐奇安、唐奇安v2 依序跑,共用 1h K 快取)。
func (s *Store) DonchianTick() {
	for _, r := range s.dcRunners() {
		s.dcTick(r)
	}
}

// dcOpenTrade 回這本書在 coin 上的持倉(沒有就 nil)。
func dcOpenTrade(b *microBook, coin string) *PaperTrade {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "open" {
			return tr
		}
	}
	return nil
}

// dcBarsFor 抓 coin 的 1h K。v2 的持倉如果進場那根往前不到 25 根(持倉超過快取範圍),
// 改抓「進場前 25 根 → 現在」整段,吊燈停利的最高 / 最低收盤和 ATR 才完整。抓不到就退回一般範圍。
func (s *Store) dcBarsFor(r dcRunner, coin string, now time.Time, loads *int) ([]exchange.Candle, *exchange.Candle, bool) {
	if r.v3 {
		return s.dcV3Bars(r, coin, now, loads)
	}
	closed, forming, ok := s.hourBars(coin, now)
	if !ok || r.chandK <= 0 || len(closed) == 0 {
		return closed, forming, ok
	}
	open := dcOpenTrade(r.b, coin)
	need := int64(dcATRN+1) * dcBarMs
	if open == nil || open.OpenTime.UnixMilli()-closed[0].Ts >= need || s.ex == nil {
		return closed, forming, ok
	}
	start := open.OpenTime.UnixMilli() - need
	cur := now.UnixMilli() / dcBarMs * dcBarMs
	v, err := s.candleCache.get(fmt.Sprintf("%s|1h|from%d|%d", coin, start, cur), func() (any, error) {
		return s.ex.BinanceKlinesRange(coin+"USDT", "1h", start, 0, 1500)
	})
	if err != nil {
		return closed, forming, ok
	}
	cs := v.([]exchange.Candle)
	if n := len(cs); n < 2 || cs[n-1].Ts != cur {
		return closed, forming, ok
	}
	last := cs[len(cs)-1]
	return cs[:len(cs)-1], &last, true
}

// dcV3Bars 回 coin 的長 1h 歷史(約 2600 根已收 + 還在走的那根)。平常用共用的 1h K 快取(200 根)接在記憶體裡的
// 歷史後面;歷史還沒有 / 接不上(斷掉超過快取範圍)就重抓一次長歷史,每分鐘最多 dcV3LoadPerMn 個幣。
// 抓不到時:有持倉就先用 200 根照樣管出場;沒持倉回 ok=false,下一分鐘再試。
func (s *Store) dcV3Bars(r dcRunner, coin string, now time.Time, loads *int) ([]exchange.Candle, *exchange.Candle, bool) {
	closed, forming, ok := s.hourBars(coin, now)
	if !ok || len(closed) == 0 {
		return nil, nil, false
	}
	if s.dcV3Hist == nil {
		s.dcV3Hist = map[string][]exchange.Candle{}
	}
	h := s.dcV3Hist[coin]
	if len(h) == 0 || h[len(h)-1].Ts < closed[0].Ts-dcBarMs {
		h = nil
		if s.ex != nil && *loads < dcV3LoadPerMn {
			*loads++
			h = s.dcV3Load(coin, now)
		}
		if len(h) == 0 {
			if dcOpenTrade(r.b, coin) != nil {
				return closed, forming, true
			}
			return nil, nil, false
		}
	}
	for _, c := range closed {
		if c.Ts > h[len(h)-1].Ts {
			h = append(h, c)
		}
	}
	if len(h) > dcV3Keep {
		h = append([]exchange.Candle(nil), h[len(h)-dcV3Keep:]...)
	}
	s.dcV3Hist[coin] = h
	return h, forming, true
}

// dcV3Load 從現在往前 dcV3Keep 根開始抓 1h K(每次最多 1500 根,抓兩次),只留已收的。
func (s *Store) dcV3Load(coin string, now time.Time) []exchange.Candle {
	cur := now.UnixMilli() / dcBarMs * dcBarMs
	from := cur - int64(dcV3Keep)*dcBarMs
	var out []exchange.Candle
	for k := 0; k < 3 && from < cur; k++ {
		cs, err := s.ex.BinanceKlinesRange(coin+"USDT", "1h", from, 0, 1500)
		if err != nil {
			return nil
		}
		for _, c := range cs {
			if c.Ts < cur && (len(out) == 0 || c.Ts > out[len(out)-1].Ts) {
				out = append(out, c)
			}
		}
		if len(cs) < 1500 || len(out) == 0 {
			break
		}
		from = out[len(out)-1].Ts + dcBarMs
	}
	return out
}

// dcV3Width 回突破前那根(closed[n-2])的通道寬度,以及過去 dcV3WidthN 根通道寬度的中位數。
// 通道寬度 = (前 336 根最高 − 最低)÷ 那根收盤。資料不足 ok=false。
func dcV3Width(closed []exchange.Candle) (w, med float64, ok bool) {
	n := len(closed)
	j1 := n - 2
	j0 := j1 - dcV3WidthN + 1
	if j0-dcV3EntryN+1 < 0 {
		return 0, 0, false
	}
	ws := make([]float64, 0, dcV3WidthN)
	for j := j0; j <= j1; j++ {
		hh, ll := closed[j].High, closed[j].Low
		for _, c := range closed[j-dcV3EntryN+1 : j] {
			hh, ll = max(hh, c.High), min(ll, c.Low)
		}
		if closed[j].Close <= 0 {
			return 0, 0, false
		}
		ws = append(ws, (hh-ll)/closed[j].Close)
	}
	w = ws[len(ws)-1]
	sort.Float64s(ws)
	m := len(ws) / 2
	med = ws[m]
	if len(ws)%2 == 0 {
		med = (ws[m-1] + ws[m]) / 2
	}
	return w, med, true
}

// dcSignalV3:收盤突破前 336 根 + 突破前通道寬度 < 過去 2000 根的中位數。
func dcSignalV3(closed []exchange.Candle, forming *exchange.Candle) (dir string, entry, sl float64, ok bool) {
	dir, entry, sl, ok = dcSignalN(closed, forming, dcV3EntryN)
	if !ok {
		return
	}
	if w, med, wok := dcV3Width(closed); !wok || w >= med {
		return "", 0, 0, false
	}
	return
}

// dcStoppedRecently:這本書同幣同方向,在 at 之前 48 小時內有沒有被初始停損(sl)打掉過。
func dcStoppedRecently(trades []*PaperTrade, coin, dir string, at int64) bool {
	for _, tr := range trades {
		if tr.Coin == coin && tr.Dir == dir && tr.Status == "closed" && tr.Outcome == "sl" && tr.CloseTime != nil &&
			tr.CloseTime.UnixMilli() > at-dcV3StopCool && tr.CloseTime.UnixMilli() <= at {
			return true
		}
	}
	return false
}

func (s *Store) dcTick(r dcRunner) {
	b := r.b
	now := time.Now().UTC()
	coins := r.eng.due(now, func() []string { return donchianCoins(b) })
	enabled := s.StrategyEnabled(b.strat())
	opened, closedN, loads := 0, 0, 0
	for _, coin := range coins {
		closed, forming, ok := s.dcBarsFor(r, coin, now, &loads)
		if !ok {
			continue
		}
		delete(r.eng.pend, coin)
		var exits, entries, dirty []*PaperTrade
		b.mu.Lock()
		var open *PaperTrade
		for _, tr := range b.trades {
			if tr.Coin == coin && tr.Status == "open" {
				open = tr
				break
			}
		}
		if open != nil {
			open.ATR = roundPx(dcATR(closed)) // 目前的 ATR(24),頁面顯示用
			open.ChanExit = dcChanLevelN(closed, open.Dir, r.exitN)
			if r.chandK > 0 {
				open.ChandExit = dcChandLevelK(open, closed, r.chandK)
			}
			gain, dd, peak := open.MaxGain, open.MaxDD, open.PeakMs
			px, outcome, at, upto, hit := dcExitScanP(open, closed, forming, r.exitN, r.chandK)
			dcExcursion(open, closed[:upto]) // 最大獲利 / 回撤只算到出場那根
			if hit {
				closeTrade(open, px, outcome, at)
				exits = append(exits, open)
				open = nil
			} else if open.MaxGain != gain || open.MaxDD != dd || open.PeakMs != peak {
				dirty = append(dirty, open)
			}
		}
		if open == nil && enabled && !r.eng.boot {
			sigFn := dcSignal
			if r.v3 {
				sigFn = dcSignalV3
			}
			sig := closed[len(closed)-1]
			if dir, entry, sl, ok := sigFn(closed, forming); ok && !(r.v3 && dcStoppedRecently(b.trades, coin, dir, sig.Ts+dcBarMs)) {
				tr := &PaperTrade{ID: fmt.Sprintf("%s|%s|%d", b.name, coin, sig.Ts+dcBarMs), Coin: coin, Dir: dir,
					Entry: entry, SL: sl, Cur: entry, Status: "open", OpenTime: time.UnixMilli(sig.Ts + dcBarMs).UTC(),
					ATR: roundPx(dcATR(closed)), ChanExit: dcChanLevelN(closed, dir, r.exitN)}
				if r.chandK > 0 {
					tr.ChandExit = dcChandLevelK(tr, closed, r.chandK) // 還沒有進場後的收盤 → 0,下一小時才有
				}
				b.trades = append(b.trades, tr)
				entries = append(entries, tr)
			}
		}
		if len(exits) > 0 {
			microTrim(b)
		}
		b.mu.Unlock()
		for _, tr := range append(append(exits, entries...), dirty...) {
			if s.db != nil {
				s.db.upsertTrade(b.name, tr)
			}
		}
		for _, tr := range exits {
			s.notifyCloseBook(b.name, tr, now, false)
		}
		for _, tr := range entries {
			s.notifyOpenBook(b.name, tr)
		}
		opened += len(entries)
		closedN += len(exits)
		time.Sleep(25 * time.Millisecond)
	}
	if opened+closedN > 0 {
		log.Printf("策略[%s] 進場 %d 筆、出場 %d 筆", b.name, opened, closedN)
	}
}

// dcSignal 看最新一根已收的 1h:收盤突破前 168 根的最高 / 最低 → 以下一根開盤進場,停損 = 進場 ∓ 3×ATR(24)。
func dcSignal(closed []exchange.Candle, forming *exchange.Candle) (dir string, entry, sl float64, ok bool) {
	return dcSignalN(closed, forming, dcEntryN)
}

// dcSignalN:同 dcSignal,進場通道用前 entryN 根。
func dcSignalN(closed []exchange.Candle, forming *exchange.Candle, entryN int) (dir string, entry, sl float64, ok bool) {
	n := len(closed)
	if n < entryN+1 || n < dcATRN+1 {
		return
	}
	sig := closed[n-1]
	hh, ll := closed[n-1-entryN].High, closed[n-1-entryN].Low
	for _, c := range closed[n-1-entryN : n-1] {
		hh, ll = max(hh, c.High), min(ll, c.Low)
	}
	switch {
	case sig.Close > hh:
		dir = "long"
	case sig.Close < ll:
		dir = "short"
	default:
		return
	}
	entry = sig.Close
	if forming != nil && forming.Ts == sig.Ts+dcBarMs && forming.Open > 0 {
		entry = forming.Open // 下一根開盤市價
	}
	atr := dcATR(closed)
	if atr <= 0 {
		return "", 0, 0, false
	}
	sl = entry - dcATRK*atr
	if dir == "short" {
		sl = entry + dcATRK*atr
	}
	return dir, roundPx(entry), roundPx(sl), true
}

// dcATR = 訊號那根(含)往前 24 根真實波幅的簡單平均。
func dcATR(closed []exchange.Candle) float64 {
	n := len(closed)
	if n < dcATRN+1 {
		return 0
	}
	sum := 0.0
	for i := n - dcATRN; i < n; i++ {
		c, pc := closed[i], closed[i-1].Close
		sum += max(c.High-c.Low, math.Abs(c.High-pc), math.Abs(c.Low-pc))
	}
	return sum / dcATRN
}

// dcExitScan 從進場那根開始一根一根重播:先看這根有沒有碰停損(跳空越過 → 開盤價),
// 再看這根收盤有沒有跌破 / 突破 12 根出場通道(→ 下一根開盤平倉)。回傳第一個出場事件。
// 出場時間記那根收盤的時刻(= 下一根開盤)。upto = 持倉期間的 K 到第幾根為止(closed[:upto])。
func dcExitScan(tr *PaperTrade, closed []exchange.Candle, forming *exchange.Candle) (px float64, outcome string, at time.Time, upto int, hit bool) {
	return dcExitScanV(tr, closed, forming, false)
}

// dcExitScanV:chand = true 時(唐奇安v2)每根收盤另外檢查吊燈停利(進場後最高 / 最低收盤 ∓ 3 × 那根的 ATR(24))。
func dcExitScanV(tr *PaperTrade, closed []exchange.Candle, forming *exchange.Candle, chand bool) (px float64, outcome string, at time.Time, upto int, hit bool) {
	k := 0.0
	if chand {
		k = dcATRK
	}
	return dcExitScanP(tr, closed, forming, dcExitN, k)
}

// dcExitScanP:出場通道用前 exitN 根;chandK > 0 時加吊燈停利(進場後最高 / 最低收盤 ∓ chandK × 那根的 ATR(24))。
func dcExitScanP(tr *PaperTrade, closed []exchange.Candle, forming *exchange.Candle, exitN int, chandK float64) (px float64, outcome string, at time.Time, upto int, hit bool) {
	from := tr.OpenTime.UnixMilli()
	long := tr.Dir == "long"
	ext := 0.0 // 進場後(含這根)的最高收盤(多)/ 最低收盤(空)
	seen := false
	nextOpen := func(i int, c exchange.Candle) float64 {
		if i+1 < len(closed) {
			return closed[i+1].Open
		} else if forming != nil && forming.Ts == c.Ts+dcBarMs && forming.Open > 0 {
			return forming.Open
		}
		return c.Close
	}
	for i, c := range closed {
		if c.Ts < from {
			continue
		}
		end := time.UnixMilli(c.Ts + dcBarMs).UTC()
		if tr.SL > 0 {
			if long && c.Low <= tr.SL {
				return min(tr.SL, c.Open), "sl", end, i + 1, true
			}
			if !long && c.High >= tr.SL {
				return max(tr.SL, c.Open), "sl", end, i + 1, true
			}
		}
		if !seen || (long && c.Close > ext) || (!long && c.Close < ext) {
			ext, seen = c.Close, true
		}
		if i >= exitN {
			lo, hi := closed[i-exitN].Low, closed[i-exitN].High
			for _, p := range closed[i-exitN : i] {
				lo, hi = min(lo, p.Low), max(hi, p.High)
			}
			if (long && c.Close < lo) || (!long && c.Close > hi) {
				return nextOpen(i, c), "channel", end, i + 1, true
			}
		}
		if chandK > 0 {
			if atr := dcATR(closed[:i+1]); atr > 0 && ((long && c.Close < ext-chandK*atr) || (!long && c.Close > ext+chandK*atr)) {
				return nextOpen(i, c), "chandelier", end, i + 1, true
			}
		}
	}
	return 0, "", time.Time{}, len(closed), false
}

// dcExcursion 用持倉期間的 K(含進場那根:進場在那根開盤)更新最大獲利 + 出現時間、最大回撤。
// 只會往更極端的方向調(重複跑、盤中即時價先更新過都沒關係)。
func dcExcursion(tr *PaperTrade, bars []exchange.Candle) {
	from := tr.OpenTime.UnixMilli()
	for _, c := range bars {
		if c.Ts < from {
			continue
		}
		fav, adv := c.High, c.Low
		if tr.Dir == "short" {
			fav, adv = c.Low, c.High
		}
		if g := r2(pnl(tr.Dir, tr.Entry, fav)); g > tr.MaxGain {
			tr.MaxGain, tr.PeakMs = g, c.Ts
		}
		if d := r2(pnl(tr.Dir, tr.Entry, adv)); d < tr.MaxDD {
			tr.MaxDD = d
		}
	}
}

// dcChanLevel 目前的通道出場價:多單 = 最近 12 根已收 K 的最低價、空單 = 最高價
// (下一根收盤破它 → 再下一根開盤平倉)。
func dcChanLevel(closed []exchange.Candle, dir string) float64 {
	return dcChanLevelN(closed, dir, dcExitN)
}

// dcChanLevelN:同 dcChanLevel,用最近 exitN 根。
func dcChanLevelN(closed []exchange.Candle, dir string, exitN int) float64 {
	n := len(closed)
	if n < exitN {
		return 0
	}
	lo, hi := closed[n-exitN].Low, closed[n-exitN].High
	for _, c := range closed[n-exitN:] {
		lo, hi = min(lo, c.Low), max(hi, c.High)
	}
	if dir == "short" {
		return roundPx(hi)
	}
	return roundPx(lo)
}

// dcChandLevel 目前的吊燈停利價:多單 = 進場後最高收盤 − 3 × 最新 ATR(24),空單 = 最低收盤 + 3 × ATR
// (下一根收盤破它 → 再下一根開盤平倉)。進場那根還沒收(沒有進場後的收盤)→ 0。
func dcChandLevel(tr *PaperTrade, closed []exchange.Candle) float64 {
	return dcChandLevelK(tr, closed, dcATRK)
}

// dcChandLevelK:同 dcChandLevel,吊燈停利用 k × ATR(24)。
func dcChandLevelK(tr *PaperTrade, closed []exchange.Candle, k float64) float64 {
	from := tr.OpenTime.UnixMilli()
	ext, seen := 0.0, false
	for _, c := range closed {
		if c.Ts < from {
			continue
		}
		if !seen || (tr.Dir == "long" && c.Close > ext) || (tr.Dir != "long" && c.Close < ext) {
			ext, seen = c.Close, true
		}
	}
	atr := dcATR(closed)
	if !seen || atr <= 0 {
		return 0
	}
	if tr.Dir == "long" {
		return roundPx(ext - k*atr)
	}
	return roundPx(ext + k*atr)
}

// DonchianMarkTick 每 20 秒:用即時價更新現價 / 最大獲利,碰到停損就出場(掛停損單的效果)。
func (s *Store) DonchianMarkTick() {
	now := time.Now().UTC()
	var px map[string]float64
	for _, r := range s.dcRunners() {
		if px == nil {
			px = s.livePrices()
		}
		s.dcMarkBook(r.b, s.fillOpenPx(px, r.b), now)
	}
}

// dcMark 用這組即時價(幣 → 價)更新唐奇安(v1)的持倉、出停損。
func (s *Store) dcMark(px map[string]float64, now time.Time) {
	s.dcMarkBook(s.donchianBook, px, now)
}

func (s *Store) dcMarkBook(b *microBook, px map[string]float64, now time.Time) {
	if len(px) == 0 {
		return
	}
	var closed []*PaperTrade
	b.mu.Lock()
	for _, tr := range b.trades {
		p := px[tr.Coin]
		if tr.Status != "open" || p <= 0 {
			continue
		}
		tr.Cur = roundPx(p)
		g := pnl(tr.Dir, tr.Entry, p)
		if r2(g) > tr.MaxGain {
			tr.MaxGain, tr.PeakMs = r2(g), now.UnixMilli()
		}
		if r2(g) < tr.MaxDD {
			tr.MaxDD = r2(g)
		}
		if tr.SL > 0 && ((tr.Dir == "long" && p <= tr.SL) || (tr.Dir == "short" && p >= tr.SL)) {
			closeTrade(tr, tr.SL, "sl", now)
			closed = append(closed, tr)
		}
	}
	if len(closed) > 0 {
		microTrim(b)
	}
	b.mu.Unlock()
	for _, tr := range closed {
		if s.db != nil {
			s.db.upsertTrade(b.name, tr)
		}
		s.notifyCloseBook(b.name, tr, now, false)
	}
}

// ---- 預估止盈(唐奇安v2 / v3 頁面顯示用,不影響出場)----

// DcEst 用唐奇安(初版)已平倉單的「最大獲利」換算成 R(= 進場到初始停損的距離)後的分位數。
// P50 = 歷史上一半的單,進場後至少漲(空單:跌)到 P50 × R。N < dcEstMinN 時不給分位數(樣本不足)。
type DcEst struct {
	N    int     `json:"n"`
	Min  int     `json:"min"`
	P25  float64 `json:"p25,omitempty"`
	P50  float64 `json:"p50,omitempty"`
	P75  float64 `json:"p75,omitempty"`
	From int64   `json:"from,omitempty"` // 樣本裡最早那筆的進場時間(ms)
}

const dcEstMinN = 30

// dcEstimate:只用正常出場的單(停損 / 通道);手動平倉會把最大獲利截斷,不算。
func dcEstimate(trades []*PaperTrade) *DcEst {
	var rs []float64
	e := &DcEst{Min: dcEstMinN}
	for _, tr := range trades {
		if tr.Status != "closed" || tr.Entry <= 0 || tr.SL <= 0 || (tr.Outcome != "sl" && tr.Outcome != "channel") {
			continue
		}
		r := math.Abs(tr.Entry-tr.SL) / tr.Entry * 100
		if r <= 0 {
			continue
		}
		if ms := tr.OpenTime.UnixMilli(); len(rs) == 0 || ms < e.From {
			e.From = ms
		}
		rs = append(rs, math.Max(tr.MaxGain, 0)/r)
	}
	e.N = len(rs)
	if e.N < dcEstMinN {
		return e
	}
	sort.Float64s(rs)
	q := func(p float64) float64 { // 線性內插的分位數
		x := p * float64(len(rs)-1)
		i := int(x)
		if i+1 >= len(rs) {
			return r2(rs[i])
		}
		return r2(rs[i] + (rs[i+1]-rs[i])*(x-float64(i)))
	}
	e.P25, e.P50, e.P75 = q(0.25), q(0.5), q(0.75)
	return e
}

func (s *Store) dcWithEst(st PaperState) PaperState {
	if b := s.donchianBook; b != nil {
		b.mu.Lock()
		st.Est = dcEstimate(b.trades)
		b.mu.Unlock()
	}
	return st
}
