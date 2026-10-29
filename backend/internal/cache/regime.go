package cache

import (
	"fmt"
	"math"
	"time"

	"datahunter/internal/exchange"
)

// regime.go — 進場時的「EMA200 與大盤環境」快照(脈衝星家族,只記錄、不影響進場)。
//
//	Ema200Dist  這個幣:進場價離自己 4H EMA200 幾 %(正 = 在上方)
//	BtcEma200   BTC:最新已收 4H 收盤離 4H EMA200 幾 %(正 = BTC 站在 EMA200 之上,大盤偏多)
//	Breadth     全市場:EMA200 監控掃描的幣(成交額前 200)裡,4H 收在 EMA200 之上的比例 %
//
// 4H EMA200 優先用 EMA200 監控引擎已算好的(同一根 4H 內不重抓);引擎沒掃到的幣才自己抓 4H K。
// 補資料工具(cmd/pulsartag)用 EMA200Dist 算同一個口徑。

// EMA200Dist 用「已收」的 4H K(舊→新)算最後一根的 EMA200,回傳 price 離它幾 %。K 線不夠回 ok=false。
func EMA200Dist(cs4h []exchange.Candle, price float64) (dist float64, ok bool) {
	if len(cs4h) < e2MinBars || price <= 0 {
		return 0, false
	}
	e := emaSeries(cs4h, e2Period)[len(cs4h)-1]
	if e <= 0 {
		return 0, false
	}
	return r2((price - e) / e * 100), true
}

// ema200Of 回傳幣的最新已收 4H 收盤與 EMA200。
func (s *Store) ema200Of(coin string) (close, ema float64, ok bool) {
	lastBar := (time.Now().UnixMilli()/e2BarMs - 1) * e2BarMs
	if s.ema200 != nil {
		s.ema200.mu.Lock()
		st, have := s.ema200.coins[coin]
		s.ema200.mu.Unlock()
		if have && st.bar == lastBar && st.ema > 0 {
			return st.close, st.ema, true
		}
	}
	v, err := s.candleCache.get(fmt.Sprintf("%s|4h|%d", coin, e2Limit), func() (any, error) {
		return s.ex.BinanceKlines(coin+"USDT", "4h", e2Limit)
	})
	if err != nil {
		return 0, 0, false
	}
	cs := v.([]exchange.Candle)
	if len(cs) < 2 {
		return 0, 0, false
	}
	cs = cs[:len(cs)-1] // 丟掉還在走的那根
	if len(cs) < e2MinBars {
		return 0, 0, false
	}
	return cs[len(cs)-1].Close, emaSeries(cs, e2Period)[len(cs)-1], true
}

// ema200Breadth = EMA200 監控掃描的幣裡,最新 4H 收在 EMA200 之上的比例 %。掃到的幣太少回 ok=false。
func (s *Store) ema200Breadth() (pct float64, ok bool) {
	if s.ema200 == nil {
		return 0, false
	}
	s.ema200.mu.Lock()
	defer s.ema200.mu.Unlock()
	above, n := 0, 0
	for _, st := range s.ema200.coins {
		if st.ema <= 0 {
			continue
		}
		n++
		if st.close > st.ema {
			above++
		}
	}
	if n < 50 {
		return 0, false
	}
	return r2(float64(above) / float64(n) * 100), true
}

// tagRegime 把 EMA200 / 大盤環境寫進新單(tagEntry 呼叫,在鎖外抓資料)。
func (s *Store) tagRegime(b *microBook, tr *PaperTrade) {
	var dist, btc, breadth float64
	if _, ema, ok := s.ema200Of(tr.Coin); ok && ema > 0 {
		dist = r2((tr.Entry - ema) / ema * 100)
	}
	if c, ema, ok := s.ema200Of("BTC"); ok && ema > 0 {
		btc = r2((c - ema) / ema * 100)
	}
	breadth, _ = s.ema200Breadth()
	b.mu.Lock()
	tr.Ema200Dist, tr.BtcEma200, tr.Breadth = dist, btc, breadth
	b.mu.Unlock()
}

// r2 四捨五入到小數 2 位(round2 是無條件捨去)。
func r2(v float64) float64 { return math.Round(v*100) / 100 }
