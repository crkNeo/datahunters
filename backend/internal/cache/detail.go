package cache

import (
	"fmt"
	"sort"

	"datahunter/internal/indicator"
	"datahunter/internal/scorer"
)

// sectors groups the coin universe so the detail card can surface peers, the
// way the reference app shows "相關幣種 · Layer 1" etc. Coins not listed fall
// into "其他".
var sectors = map[string]string{
	// Layer 1
	"BTC": "Layer 1", "ETH": "Layer 1", "SOL": "Layer 1", "BNB": "Layer 1",
	"XRP": "Layer 1", "ADA": "Layer 1", "AVAX": "Layer 1", "SUI": "Layer 1",
	"LTC": "Layer 1", "DOT": "Layer 1", "TRX": "Layer 1", "NEAR": "Layer 1",
	"APT": "Layer 1", "ATOM": "Layer 1", "TON": "Layer 1", "ICP": "Layer 1",
	"FIL": "Layer 1", "SEI": "Layer 1", "TIA": "Layer 1", "BCH": "Layer 1",
	// Layer 2
	"ARB": "Layer 2", "OP": "Layer 2",
	// DeFi
	"LINK": "DeFi", "UNI": "DeFi", "AAVE": "DeFi", "ENA": "DeFi",
	"JUP": "DeFi", "INJ": "DeFi",
	// Meme
	"DOGE": "迷因", "SHIB": "迷因", "PEPE": "迷因", "WIF": "迷因", "TRUMP": "迷因",
	// AI / infra
	"WLD": "AI/基建", "FET": "AI/基建",
}

func sectorOf(coin string) string {
	if s, ok := sectors[coin]; ok {
		return s
	}
	return "其他"
}

// Stats are the headline metric cards on the detail view.
type Stats struct {
	Chg24h  float64 `json:"chg_24h"`
	Funding float64 `json:"funding_rate"`
	OIValue float64 `json:"oi_value"` // open interest, USDT notional
	LongPct float64 `json:"long_pct"`
	Vol24h  float64 `json:"vol_24h"` // trailing 24h quote (USDT) volume
}

// RelatedCoin is one peer in the 相關幣種 strip.
type RelatedCoin struct {
	Coin   string  `json:"coin"`
	Sector string  `json:"sector"`
	Score  int     `json:"score"`
	Chg    float64 `json:"chg"`
}

// CoinDetail is the full payload for the expandable per-coin card.
type CoinDetail struct {
	Coin      string                 `json:"coin"`
	Total     int                    `json:"total"`
	Raw       int                    `json:"raw"`
	LiqFactor float64                `json:"liq_factor"`
	Rating    int                    `json:"rating"`
	Bias      string                 `json:"bias"`
	BiasLabel string                 `json:"bias_label"`
	Sector    string                 `json:"sector"`
	Rationale []scorer.Rationale     `json:"rationale"`
	Breakdown []scorer.BreakdownItem `json:"breakdown"`
	Stats     Stats                  `json:"stats"`
	Related   []RelatedCoin          `json:"related"`
}

// Detail returns the cached score card for a tracked coin. For an untracked
// coin (e.g. one clicked from the full contract table) it is computed live with
// the same scorer, so it is always self-consistent.
func (s *Store) Detail(coin string) (CoinDetail, error) {
	s.mu.RLock()
	d, ok := s.details[coin]
	s.mu.RUnlock()
	if ok {
		return d, nil // monitored coin: already refreshed on the ticker
	}

	// non-monitored coin: live fetch, but shared-cached (30s) + singleflight so
	// many users clicking the same coin trigger one fetch, not one each.
	v, err := s.detailCache.get(coin, func() (any, error) {
		t, _ := s.ex.Binance24h(coin + "USDT")
		btc, _ := s.ex.Binance24h("BTCUSDT")
		cd, _ := s.computeDetailCore(coin, t.ChangePct, btc.ChangePct)
		if cd.Total == 0 && cd.Stats.OIValue == 0 {
			return nil, fmt.Errorf("no data for %s", coin) // not on our venues
		}
		cd.Related = s.relatedCoins(coin)
		return cd, nil
	})
	if err != nil {
		return CoinDetail{}, err
	}
	return v.(CoinDetail), nil
}

// computeDetailCore fetches fresh data for one coin, scores it with the detail
// scorer, and returns both the full card (without related peers) and the board
// snapshot derived from the SAME result. mom24h and btcChg are supplied by the
// caller (from one batched all-tickers call during Refresh).
func (s *Store) computeDetailCore(coin string, mom24h, btcChg float64) (CoinDetail, Snapshot) {
	inst := coin + "-USDT-SWAP"

	funding, _ := s.ex.OKXFundingRate(inst)
	ls := s.longShortCached(coin)    // cached 5 min (no WS)
	kl := s.klines1hCached(coin, 24) // cached 4 min (shared) — avoids 418 ban
	oiHist := s.oiHist1h(coin, 2)    // cached 5 min (no WS)

	// 1h price momentum from the latest 1H bar
	var mom1h float64
	if len(kl) >= 1 {
		last := kl[len(kl)-1]
		mom1h = indicator.PctChange(last.Open, last.Close)
	}

	structLabel, structDir := indicator.PriceStructure(kl)

	var oiChg1h, oiValue float64
	if len(oiHist) >= 2 {
		oiChg1h = indicator.PctChange(oiHist[0].SumOIValue, oiHist[len(oiHist)-1].SumOIValue)
		oiValue = oiHist[len(oiHist)-1].SumOIValue
	} else if len(oiHist) == 1 {
		oiValue = oiHist[0].SumOIValue
	}

	// CVD over the last 12h, from kline taker-buy volume (consistent across coins)
	cvdRatio := indicator.CVDFromKlines(kl, 12)

	// trailing 24h quote volume, for liquidity damping
	var vol24 float64
	for _, c := range kl {
		vol24 += c.QuoteVol
	}

	res := scorer.ScoreDetail(scorer.DetailInput{
		Coin:        coin,
		OIChg1h:     oiChg1h,
		CVDRatio:    cvdRatio,
		StructLabel: structLabel,
		StructDir:   structDir,
		Mom1h:       mom1h,
		Mom24h:      mom24h,
		FundingRate: funding,
		LongAccount: ls.LongAccount,
		RelStrength: mom24h - btcChg,
		Vol24h:      vol24,
	}, scorer.DefaultDetailWeights())

	detail := CoinDetail{
		Coin:      coin,
		Total:     res.Total,
		Raw:       res.Raw,
		LiqFactor: res.LiqFactor,
		Rating:    res.Rating,
		Bias:      res.Bias,
		BiasLabel: res.BiasLabel,
		Sector:    sectorOf(coin),
		Rationale: res.Rationale,
		Breakdown: res.Breakdown,
		Stats: Stats{
			Chg24h:  round2(mom24h),
			Funding: funding,
			OIValue: oiValue,
			LongPct: round2(ls.LongAccount * 100),
			Vol24h:  vol24,
		},
	}

	snap := Snapshot{
		OKXChg:   round2(mom1h),
		OIChg1h:  round2(oiChg1h),
		CVDRatio: round2(cvdRatio),
		Funding:  funding,
		Score:    res.Total,
		Bias:     res.Bias,
		Quality:  qualityOf(res.Breakdown),
	}
	return detail, snap
}

// qualityOf derives the board quality tier from how many factors fired hard.
func qualityOf(bd []scorer.BreakdownItem) string {
	strong := 0
	for _, it := range bd {
		if it.Score >= 8 || it.Score <= -8 {
			strong++
		}
	}
	switch {
	case strong >= 4:
		return "高品質"
	case strong >= 2:
		return "一般"
	default:
		return "觀察"
	}
}

// relatedCoins returns same-sector peers using the current board snapshot.
func (s *Store) relatedCoins(coin string) []RelatedCoin {
	data, _ := s.All()
	return relatedFrom(coin, data)
}

// relatedFrom ranks same-sector peers by score magnitude from a snapshot map.
func relatedFrom(coin string, snaps map[string]Snapshot) []RelatedCoin {
	sec := sectorOf(coin)
	var out []RelatedCoin
	for c, snap := range snaps {
		if c == coin || sectorOf(c) != sec {
			continue
		}
		out = append(out, RelatedCoin{Coin: c, Sector: sec, Score: snap.Score, Chg: snap.OKXChg})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].Score, out[j].Score
		if ai < 0 {
			ai = -ai
		}
		if aj < 0 {
			aj = -aj
		}
		return ai > aj
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}
