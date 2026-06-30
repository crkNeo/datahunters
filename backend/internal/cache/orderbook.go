package cache

import (
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// OrderBookRow is the wall/imbalance read for one coin (current snapshot).
type OrderBookRow struct {
	Coin     string  `json:"coin"`
	Mid      float64 `json:"mid"`
	BidUSD   float64 `json:"bid_usd"`  // resting bid notional within ±2% of mid
	AskUSD   float64 `json:"ask_usd"`  // resting ask notional within ±2% of mid
	Imbal    float64 `json:"imbal"`    // bid fraction 0..1 (>0.5 = support-heavy)
	BidWall  float64 `json:"bid_wall"` // biggest bid level price (support)
	BidWallU float64 `json:"bid_wall_usd"`
	BidDist  float64 `json:"bid_dist"` // % below mid
	AskWall  float64 `json:"ask_wall"` // biggest ask level price (resistance)
	AskWallU float64 `json:"ask_wall_usd"`
	AskDist  float64 `json:"ask_dist"` // % above mid
}

// OrderBookData is the /api/orderbook payload.
type OrderBookData struct {
	Rows      []OrderBookRow `json:"rows"`
	UpdatedAt string         `json:"updated_at"`
	Note      string         `json:"note"`
}

// computeOrderBookRow turns a depth snapshot into wall/imbalance metrics.
// Considers levels within ±band of mid; a "wall" is the single largest level.
func computeOrderBookRow(coin string, d exchange.Depth) (OrderBookRow, bool) {
	if len(d.Bids) == 0 || len(d.Asks) == 0 {
		return OrderBookRow{}, false
	}
	mid := (d.Bids[0].Px + d.Asks[0].Px) / 2
	if mid <= 0 {
		return OrderBookRow{}, false
	}
	const band = 0.02 // ±2% of mid
	r := OrderBookRow{Coin: coin, Mid: mid}
	for _, b := range d.Bids {
		if b.Px < mid*(1-band) {
			break
		}
		usd := b.Px * b.Qty
		r.BidUSD += usd
		if usd > r.BidWallU {
			r.BidWallU, r.BidWall = usd, b.Px
		}
	}
	for _, a := range d.Asks {
		if a.Px > mid*(1+band) {
			break
		}
		usd := a.Px * a.Qty
		r.AskUSD += usd
		if usd > r.AskWallU {
			r.AskWallU, r.AskWall = usd, a.Px
		}
	}
	if r.BidUSD+r.AskUSD <= 0 {
		return OrderBookRow{}, false
	}
	r.Imbal = round2(r.BidUSD / (r.BidUSD + r.AskUSD))
	r.BidUSD, r.AskUSD = round2(r.BidUSD), round2(r.AskUSD)
	r.BidWallU, r.AskWallU = round2(r.BidWallU), round2(r.AskWallU)
	if r.BidWall > 0 {
		r.BidDist = round2((mid - r.BidWall) / mid * 100)
	}
	if r.AskWall > 0 {
		r.AskDist = round2((r.AskWall - mid) / mid * 100)
	}
	r.Mid = round2(mid)
	return r, true
}

// OrderBook returns the cached wall/imbalance board, refreshing if older than 60s.
func (s *Store) OrderBook() OrderBookData {
	s.obMu.Lock()
	defer s.obMu.Unlock()
	if time.Since(s.obTime) < 60*time.Second && len(s.obData.Rows) > 0 {
		return s.obData
	}
	now := time.Now()
	rows := make([]OrderBookRow, 0, len(s.coins))
	for _, coin := range s.coins {
		d, err := s.ex.BinanceDepth(coin+"USDT", 500)
		if err != nil {
			continue
		}
		if r, ok := computeOrderBookRow(coin, d); ok {
			rows = append(rows, r)
			if s.db != nil {
				s.db.insertOrderBook(now, r)
			}
		}
		time.Sleep(40 * time.Millisecond) // be polite
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Imbal > rows[j].Imbal })
	if len(rows) > 0 {
		s.obData = OrderBookData{
			Rows:      rows,
			UpdatedAt: now.Format(time.RFC3339),
			Note:      "訂單簿大牆/失衡(±2% 內掛單,即時)· 非回測訊號;已往 SQLite 累積",
		}
		s.obTime = now
	}
	return s.obData
}
