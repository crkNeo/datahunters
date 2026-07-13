package cache

import (
	"fmt"
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// convergence.go: the "動態 ATR 4H 均線收斂" strategy (admin, display-only, long+short).
//
//	進場: 4H 價格在 EMA200 同側,連續 ~4 根橫盤(區間 <= convConsoATR×ATR),且該 4 根
//	      靠 EMA200 的極值離 EMA200 不超過 1.5×ATR(動態空間限制,取代固定 3%)。
//	止損: 4 根盤整區的極值 ± 0.3×ATR(結構止損 + 掃針緩衝)。
//	止盈: VRVP 近似——沿高低價攤平成交量分箱,取進場價「上方(多)/下方(空)」最近的高量節點(POC/大量區邊緣)。
//	濾網: 預期盈虧比 (TP距離/SL距離) >= 1.5 才開倉。
//	出場: 收盤 K 觸及 TP / SL(同根同觸算 SL),或超過 convExpiryBars 根市價結算。
//
// 4H, evaluated once per closed bar. Universe = 銀河 (emaCoins). Uses atrSeries /
// emaSeries from the cache package (scanpool.go / paper_ema.go).

const (
	convKlimit      = 260
	convMinBars     = 210
	convConsoBars   = 4    // consolidation window (bars)
	convConsoATR    = 3.0  // 4-bar range must be <= this × ATR (橫盤)
	convEmaDistATR  = 1.5  // 4-bar EMA-side extreme within this × ATR of EMA200
	convSLbufATR    = 0.3  // structure-stop buffer beyond the 4-bar extreme
	convMinRR       = 1.5  // minimum reward:risk to open
	convVPWindow    = 96   // volume-profile lookback (bars)
	convVPBins      = 60   // volume-profile price bins
	convNodeFrac    = 0.6  // a "high-volume node" is a bin with vol >= frac × POC vol
	convExpiryBars  = 60   // ~10 days on 4H → settle at market
	convKeepClosed  = 500
	conv4hMs        = 4 * 3600 * 1000
)

// volProfile builds a price→volume histogram over the last window bars by spreading
// each bar's volume across its [low, high] range. Returns (vol bins, low, binWidth).
func volProfile(cs []exchange.Candle, window int) ([]float64, float64, float64) {
	n := len(cs)
	start := n - window
	if start < 0 {
		start = 0
	}
	lo, hi := math.MaxFloat64, 0.0
	for i := start; i < n; i++ {
		if cs[i].Low < lo {
			lo = cs[i].Low
		}
		if cs[i].High > hi {
			hi = cs[i].High
		}
	}
	if hi <= lo {
		return nil, 0, 0
	}
	bw := (hi - lo) / float64(convVPBins)
	vol := make([]float64, convVPBins)
	for i := start; i < n; i++ {
		b0 := int((cs[i].Low - lo) / bw)
		b1 := int((cs[i].High - lo) / bw)
		if b0 < 0 {
			b0 = 0
		}
		if b1 >= convVPBins {
			b1 = convVPBins - 1
		}
		per := cs[i].Volume / float64(b1-b0+1)
		for b := b0; b <= b1; b++ {
			vol[b] += per
		}
	}
	return vol, lo, bw
}

// nearestNode returns the nearest high-volume-node price above/below `entry`.
func nearestNode(vol []float64, lo, bw, entry float64, above bool) (float64, bool) {
	var maxv float64
	for _, v := range vol {
		if v > maxv {
			maxv = v
		}
	}
	if maxv <= 0 {
		return 0, false
	}
	th := convNodeFrac * maxv
	best, found := 0.0, false
	for b, v := range vol {
		if v < th {
			continue
		}
		price := lo + (float64(b)+0.5)*bw
		if above && price > entry && (!found || price < best) {
			best, found = price, true
		} else if !above && price < entry && (!found || price > best) {
			best, found = price, true
		}
	}
	return best, found
}

// convSignal evaluates entry on the latest closed bar. ok=false → no trade.
func convSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	ema := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema <= 0 {
		return
	}
	price := cs[n-1].Close
	// consolidation: last convConsoBars are a tight range
	hi4, lo4 := 0.0, math.MaxFloat64
	for i := n - convConsoBars; i < n; i++ {
		if i < 0 {
			return
		}
		if cs[i].High > hi4 {
			hi4 = cs[i].High
		}
		if cs[i].Low < lo4 {
			lo4 = cs[i].Low
		}
	}
	if hi4-lo4 > convConsoATR*atr {
		return
	}
	vol, vlo, bw := volProfile(cs, convVPWindow)
	if bw <= 0 {
		return
	}
	switch {
	case price > ema: // long: 4-bar HIGH within 1.5 ATR of EMA200
		if hi4-ema > convEmaDistATR*atr {
			return
		}
		s := lo4 - convSLbufATR*atr
		t, has := nearestNode(vol, vlo, bw, price, true)
		if !has || s >= price {
			return
		}
		if (t-price)/(price-s) < convMinRR {
			return
		}
		return "long", roundPx(price), roundPx(s), roundPx(t), true
	case price < ema: // short: 4-bar LOW within 1.5 ATR of EMA200
		if ema-lo4 > convEmaDistATR*atr {
			return
		}
		s := hi4 + convSLbufATR*atr
		t, has := nearestNode(vol, vlo, bw, price, false)
		if !has || s <= price {
			return
		}
		if (price-t)/(s-price) < convMinRR {
			return
		}
		return "short", roundPx(price), roundPx(s), roundPx(t), true
	}
	return
}

// ConvTick evaluates the strategy once per newly closed 4H bar over 銀河 coins.
func (s *Store) ConvTick() {
	now := time.Now().UTC()
	b4 := now.Unix() / (4 * 3600)
	if b4 == s.conv4hBucket {
		return
	}
	s.conv4hBucket = b4
	if !s.convSeeded { // boot: set the baseline only; don't backfill entries from the pre-startup bar
		s.convSeeded = true
		return
	}
	for _, coin := range s.emaCoins() {
		cs, err := s.ex.BinanceKlines(coin+"USDT", "4h", convKlimit)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // drop the still-forming bar
		if len(cs) < convMinBars {
			continue
		}
		s.runConv(coin, cs, now)
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Store) runConv(coin string, cs []exchange.Candle, now time.Time) {
	last := cs[len(cs)-1]
	s.convMu.Lock()
	var open *PaperTrade
	for _, tr := range s.convTrades {
		if tr.Coin == coin && tr.Status == "open" {
			open = tr
			break
		}
	}
	var dirty *PaperTrade
	if open != nil {
		// bar-close backstop (partial TP1/TP2 booked on the live ConvMarkTick).
		// Full-close only: final target / current stop / expiry.
		exit, outcome, px := false, "", 0.0
		if open.Dir == "long" {
			if last.Low <= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.High >= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		} else {
			if last.High >= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.Low <= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		}
		if !exit && (last.Ts-open.OpenTime.UnixMilli())/conv4hMs >= convExpiryBars {
			exit, outcome, px = true, "expired", last.Close
		}
		if exit {
			if outcome == "tp3" {
				open.Legs = 3
			}
			closeTrade(open, px, outcome, now) // blends any realized tranches
		} else {
			open.Cur = roundPx(last.Close)
			open.PnLPct = round2(open.Realized + (1-open.Filled)*pnl(open.Dir, open.Entry, last.Close))
		}
		dirty = open
	} else if dir, entry, sl, tp, ok := convSignal(cs); ok && s.StrategyEnabled("conv") {
		tr := &PaperTrade{
			ID:       fmt.Sprintf("conv|%s|%d", coin, now.UnixMilli()),
			Coin:     coin,
			Dir:      dir,
			Entry:    entry,
			SL:       sl,
			TP:       tp,
			Cur:      entry,
			Status:   "open",
			OpenTime: time.UnixMilli(last.Ts).UTC(),
		}
		setupTP(tr, tpMomentum) // 分批止盈: TP1/TP2 at 40%/70% of entry→POC target
		s.convTrades = append(s.convTrades, tr)
		dirty = tr
		s.convTrim()
	}
	s.convMu.Unlock()
	if dirty != nil && s.db != nil {
		s.db.upsertTrade("conv", dirty)
	}
}

// ConvMarkTick marks open positions to the live WS price and books partial TPs /
// exits (分批止盈) intrabar. Entries are still evaluated per closed 4H bar in
// ConvTick; the closed-bar backstop in runConv covers a feed outage.
func (s *Store) ConvMarkTick() {
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	var dirty []*PaperTrade
	s.convMu.Lock()
	for _, tr := range s.convTrades {
		if tr.Status != "open" {
			continue
		}
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		before := tr.Legs
		if closed := stepTP(tr, p, tpMomentum, now); closed || tr.Legs != before {
			dirty = append(dirty, tr) // persist on any leg change or final close
		}
		if tr.Legs > before { // a TP just filled → 軟體通知 (admin book)
			s.notifyTPHit("conv", tr, true, tr.Legs)
		}
	}
	s.convMu.Unlock()
	if s.db != nil {
		for _, tr := range dirty {
			s.db.upsertTrade("conv", tr)
		}
	}
}

func (s *Store) convTrim() {
	var open, closed []*PaperTrade
	for _, tr := range s.convTrades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > convKeepClosed {
		closed = closed[:convKeepClosed]
	}
	s.convTrades = append(open, closed...)
}

// ConvState returns the strategy's simulated open/closed/stats.
func (s *Store) ConvState() PaperState {
	px := s.livePrices() // read before the lock; open positions get live 現價 (strategy only ticks per 4H bar)
	s.convMu.Lock()
	defer s.convMu.Unlock()
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	st.Stats.MultiTP = true
	var sum, grossWin, grossLoss float64
	for _, tr := range s.convTrades {
		if tr.Status == "open" {
			st.Open = append(st.Open, tr)
			continue
		}
		st.Closed = append(st.Closed, tr)
		st.Stats.Closed++
		sum += tr.PnLPct
		if tr.PnLPct > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
		tpStats(tr, &st.Stats.Tp1, &st.Stats.Tp2, &st.Stats.Tp3, &grossWin, &grossLoss)
	}
	markLiveOpen(st.Open, px) // display-only: live 現價 between 4H bars
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool {
		return st.Closed[i].CloseTime != nil && st.Closed[j].CloseTime != nil && st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime)
	})
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
		st.Stats.AvgPnl = round2(sum / float64(st.Stats.Closed))
		st.Stats.TotalPnl = round2(sum)
		if grossLoss > 0 {
			st.Stats.ProfitFactor = round2(grossWin / grossLoss)
		} else if grossWin > 0 {
			st.Stats.ProfitFactor = 99.99
		}
	}
	return st
}
