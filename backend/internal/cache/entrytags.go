package cache

import (
	"math"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// entrytags.go — 進場因子快照:脈衝星(v3/v12)每筆進場時,把爆發雷達與多空推薦評分器用到的
// 因子記下來(不影響進場),之後拿來分組比較「哪個條件真的能讓單變好」。
// 線上(tagEntry)與補資料工具(cmd/pulsartag)共用 ComputeEntryTags,兩邊口徑一致。

// EntryTags 是一筆單進場當下的因子。0 = 沒資料。
type EntryTags struct {
	OI12h    float64 // 近 12h 名目 OI 變化%(雷達的「OI 堆積」)
	OI1h     float64 // 近 1h 名目 OI 變化%(評分器的市場結構)
	CVD6h    float64 // 近 6h 主動買賣差 / 成交量 %(雷達口徑)
	CVD1h    float64 // 近 1h 同上(看力竭/轉向)
	Funding  float64 // 進場前最近一次已結算的資金費率(原始值,0.0001 = 0.01%)
	LongAcct float64 // 散戶做多帳戶比例 0..1
	Chg24    float64 // 24h 漲跌%
	SurgeX   float64 // 爆量倍數(近 30 分鐘成交額 / 自身 8h 基線)
	BTC1h    float64 // BTC 現價對上一個整點收盤的漲跌%
	Vol24    float64 // 近 24h 成交額(USDT)

	// 進場那根 K(盤中進場 = 到進場為止的部分 K)的形狀與位置
	WickPct float64 // 上影線佔這根全距的 %(越大 = 已從高點被砸回越多)
	EMADist float64 // (現價 − 15m EMA20) / ATR:離均線幾個 ATR(越大 = 追得越高)
	BarATR  float64 // 這根全距 / ATR:這根有多誇張
	SpikeX  float64 // 近 6 根(含這根)最大量 / 截尾基線量:觸發的爆量是幾倍
	BTC15m  float64 // BTC 現價對上一根 15m 收盤的漲跌%
}

// ComputeEntryTags 由原始資料算出因子。
//
//	cs15:進場當下的 15m K(最後一根 = 進場那根;盤中進場時是到進場為止的部分 K)
//	oi1h:1h 名目 OI 歷史(舊→新,最後一點 = 進場前最近的整點)
//	btc1h:BTC 漲跌%(已換算好)
func ComputeEntryTags(cs15 []exchange.Candle, oi1h []exchange.OIPoint, funding, longAcct, btc1h, btc15m, surgeX float64) EntryTags {
	t := EntryTags{Funding: funding, LongAcct: longAcct, BTC1h: btc1h, BTC15m: btc15m, SurgeX: surgeX}
	if len(oi1h) > 13 {
		oi1h = oi1h[len(oi1h)-13:]
	}
	if n := len(oi1h); n >= 2 {
		t.OI12h = indicator.PctChange(oi1h[0].SumOIValue, oi1h[n-1].SumOIValue)
		t.OI1h = indicator.PctChange(oi1h[n-2].SumOIValue, oi1h[n-1].SumOIValue)
	}
	t.CVD6h = indicator.CVDFromKlines(cs15, 24)
	t.CVD1h = indicator.CVDFromKlines(cs15, 4)
	n := len(cs15)
	if n >= 97 {
		t.Chg24 = indicator.PctChange(cs15[n-97].Close, cs15[n-1].Close)
	}
	for i := n - 96; i < n; i++ {
		if i >= 0 {
			t.Vol24 += cs15[i].QuoteVol
		}
	}
	if n > 0 {
		last := cs15[n-1]
		if rng := last.High - last.Low; rng > 0 {
			t.WickPct = (last.High - math.Max(last.Open, last.Close)) / rng * 100
		}
		if atr := robustATR(cs15, 14, 2); atr > 0 {
			t.EMADist = (last.Close - emaSeries(cs15, 20)[n-1]) / atr
			t.BarATR = (last.High - last.Low) / atr
		}
		if base := trimmedBaseVol(cs15); base > 0 {
			peak := 0.0
			for i := n - 6; i < n; i++ {
				if i >= 0 {
					peak = math.Max(peak, cs15[i].Volume)
				}
			}
			t.SpikeX = peak / base
		}
	}
	return t
}

// apply 寫進交易紀錄。OI/CVD/Funding 沿用 PaperTrade 既有三欄(與雷達單同口徑)。
func (t EntryTags) apply(tr *PaperTrade) {
	tr.OI, tr.CVD, tr.Funding = t.OI12h, t.CVD6h, t.Funding
	tr.OI1h, tr.CVD1h, tr.LongAcct = t.OI1h, t.CVD1h, t.LongAcct
	tr.Chg24, tr.SurgeX, tr.BTC1h, tr.Vol24 = t.Chg24, t.SurgeX, t.BTC1h, t.Vol24
	tr.WickPct, tr.EMADist, tr.BarATR, tr.SpikeX, tr.BTC15m = t.WickPct, t.EMADist, t.BarATR, t.SpikeX, t.BTC15m
}

// SurgeXFromMinuteKlines 用進場前的 1m K 重建爆量脈搏的快照環,算出當時的爆量倍數。
// 與線上 surgeEngine 同一套(reconstructSurgeSeed + pulseAndBaseline)。資料不足回 0。
func SurgeXFromMinuteKlines(ks []exchange.Candle) float64 {
	points := surgeRing
	if max := len(ks) - 1440; max < points { // reconstructSurgeSeed 需要 24h(1440 根)+ points(step=1)
		points = max
	}
	if points < surgeMinHist {
		return 0
	}
	recent, base, ok := pulseAndBaseline(reconstructSurgeSeed(ks, points), surgeWin)
	if !ok || base <= 0 {
		return 0
	}
	return recent / base
}

// SurgeV3OK 回報 v3 的進場條件在這組 K 線(最後一根 = 已收的訊號根)上是否成立。
// 給補資料工具判斷盤中單(v12)「收盤後 v3 是否也成立」。
func SurgeV3OK(cs []exchange.Candle) bool {
	_, _, _, _, ok := surgeV3Signal(cs)
	return ok
}

// tagEntry 抓進場當下的因子寫進新單(只對開了 tagEntry 的書)。資料在鎖外抓,寫回時才拿 b.mu;
// 呼叫端要在第一次 upsertTrade 之前呼叫,因子才會跟著 INSERT 進 DB。
func (s *Store) tagEntry(b *microBook, tr *PaperTrade, cs []exchange.Candle) {
	if !b.tagEntry || tr == nil {
		return
	}
	coin := tr.Coin
	btc, _ := s.btc1hRet()
	btc15, _ := s.btc15mRet()
	tags := ComputeEntryTags(cs, s.oiHist1h(coin, 13), s.lastFunding(coin), s.longShortCached(coin).LongAccount, btc*100, btc15*100, s.surgeXOf(coin))
	b.mu.Lock()
	tags.apply(tr)
	b.mu.Unlock()
}

// btc15mRet 回傳 BTC 現價對上一根 15m 收盤的報酬(小數),快取 30 秒(15m 的指標不能用太舊的值)。
func (s *Store) btc15mRet() (float64, bool) {
	v, err := s.klineCache.get("btc15mret", func() (any, error) {
		kl, e := s.ex.BinanceKlines("BTCUSDT", "15m", 2)
		if e != nil || len(kl) < 2 || kl[len(kl)-2].Close <= 0 {
			return 0.0, e
		}
		return kl[len(kl)-1].Close/kl[len(kl)-2].Close - 1, nil
	})
	if err != nil {
		return 0, false
	}
	ret, _ := v.(float64)
	return ret, true
}

// lastFunding 回傳最近一次已結算的資金費率(快取 10 分鐘;費率 8h 才結算一次)。
func (s *Store) lastFunding(coin string) float64 {
	v, err := s.oiCache.get("fr|"+coin, func() (any, error) {
		pts, e := s.ex.BinanceFundingHist(coin+"USDT", 1)
		if e != nil || len(pts) == 0 {
			return 0.0, e
		}
		return pts[len(pts)-1].Rate, nil
	})
	if err != nil {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// surgeXOf 回傳某幣目前的爆量倍數(不套面板的地板過濾)。沒有足夠歷史回 0。
func (s *Store) surgeXOf(coin string) float64 {
	if s.surge == nil {
		return 0
	}
	s.surge.mu.Lock()
	defer s.surge.mu.Unlock()
	recent, base, ok := pulseAndBaseline(s.surge.hist[coin], surgeWin)
	if !ok || base <= 0 {
		return 0
	}
	return recent / base
}
