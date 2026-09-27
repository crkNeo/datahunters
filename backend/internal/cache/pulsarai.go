package cache

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

// pulsarai.go: 脈衝星v3 每個「新開的進場訊號」找 AI 出一個 1–100 的參考信心分數,
// 給使用者當第二意見(非勝率保證、不影響任何自動下單)。
//
// 為什麼有用:脈衝星v3 的進場邏輯只看單一 15m 圖(爆量+動能),對「大盤方向、4H 趨勢、
// 上方壓力、資金費率擁擠度」完全是瞎的。AI 的價值就是補這塊——分數同時反映
// ①進場點品質 + ②5R 目標打得到的機會(上方空間夠不夠、趨勢撐不撐得住延續)。
//
// 設計:結果存記憶體(store.aiConf,transient/不進 DB),每個訊號只算一次。伺服時
// 若某個 open 單還沒有分數就非同步補算——重啟後仍會自癒補回。沒設 GROQ_API_KEY
// (Provider()=="none") 時整個功能靜默停用,前端 chip 自然隱藏。

type pulsarAI struct {
	Score   int      `json:"score"`
	Tag     string   `json:"tag"`
	Reasons []string `json:"reasons"`
}

const pulsarAISystem = "你是加密永續合約的風控助手。使用者用一個「15m 爆量突破、只做多」的機械策略進場," +
	"該策略已確保:動能向上、爆量、非拋物線頂、未過度追高、止損可行——你不用重複確認這些。" +
	"你的任務是評估這筆突破在「策略看不到的更大環境」下的品質,給一個 1–100 的參考信心分數," +
	"綜合兩件事:①這個進場點值不值得進(環境順不順),②這筆單的 5R 獲利目標有沒有機會打到" +
	"(上方到壓力的空間夠不夠、4H 趨勢撐不撐得住延續)。\n" +
	"錨定:80+ = 4H 同向上升 + 上方空間 ≥ 目標所需 + 大盤順勢 + 資金費率正常;" +
	"~50 = 能進但有一項明顯扣分(上方很快撞壓力/4H 只是反彈/已追高);" +
	"20↓ = 逆 4H 或逆大盤 / 上方幾乎沒空間(目標難達成)/ 資金費率極端。\n" +
	"只輸出 JSON,格式:{\"score\":整數1-100,\"tag\":\"≤8字標籤\",\"reasons\":[\"2到4條白話理由,每條對應數字\"]}。" +
	"理由用繁體中文,不要多餘文字。"

// ensurePulsarAIEnabled reports whether an AI backend is actually configured.
func (s *Store) pulsarAIEnabled() bool {
	return s.maiW != nil && s.maiW.Provider() != "none"
}

// attachPulsarAI 把已算好的分數填進 open 單(transient 欄位);還沒算的就非同步觸發。
func (s *Store) attachPulsarAI(open []*PaperTrade) {
	if !s.pulsarAIEnabled() {
		return
	}
	for _, tr := range open {
		if v, ok := s.aiConf.Load(tr.ID); ok {
			c := v.(*pulsarAI)
			tr.AIScore, tr.AITag, tr.AIReasons = c.Score, c.Tag, c.Reasons
			continue
		}
		// 尚未算 → 觸發。aiBusy 存「上次嘗試時刻」:成功會寫進 aiConf(此後命中快取、永不重試);
		// 失敗則靠這個時間戳退避 pulsarAIRetry,避免前端每次輪詢就重打一次 API(重試風暴)。
		if v, ok := s.aiBusy.Load(tr.ID); ok && time.Since(v.(time.Time)) < pulsarAIRetry {
			continue
		}
		s.aiBusy.Store(tr.ID, time.Now())
		go s.computePulsarAI(tr.ID, tr.Coin, tr.Entry, tr.SL, tr.TP)
	}
}

// pulsarAIRetry 是單筆進場算分失敗後的最短重試間隔(前端輪詢頻繁,沒有退避會變重試風暴)。
const pulsarAIRetry = 60 * time.Second

// computePulsarAI 組 context → 呼叫 AI → 存結果。失敗就放掉 busy 旗標,下次伺服再試。
func (s *Store) computePulsarAI(id, coin string, entry, sl, tp float64) {
	if entry <= 0 {
		return
	}
	// 15m + 4h K 線(脈衝星是 15m 進場;4h 判趨勢/壓力)。直接抓、不走快取——
	// 每個新訊號才算一次,頻率低(每幣 cooldown 4h),兩個 REST 可接受。
	cs15, err := s.ex.BinanceKlines(coin+"USDT", "15m", 200)
	if err != nil || len(cs15) < 100 {
		log.Printf("pulsar-ai: %s 15m klines unavailable (n=%d err=%v) — skip", coin, len(cs15), err)
		return
	}
	cs4, err := s.ex.BinanceKlines(coin+"USDT", "4h", 120)
	if err != nil || len(cs4) < 30 {
		log.Printf("pulsar-ai: %s 4h klines unavailable (n=%d err=%v) — skip", coin, len(cs4), err)
		return
	}
	in := s.buildPulsarAIInput(coin, entry, sl, tp, cs15, cs4)
	raw, err := s.maiW.Analyze(pulsarAISystem, in)
	if err != nil {
		log.Printf("pulsar-ai: %s analyze failed: %v", coin, err)
		return
	}
	c := parsePulsarAI(raw)
	if c == nil {
		log.Printf("pulsar-ai: %s unparseable reply: %.120q", coin, raw)
		return
	}
	s.aiConf.Store(id, c)
}

// buildPulsarAIInput 把進場當下的 A+B context 組成餵給 AI 的 JSON 字串。
func (s *Store) buildPulsarAIInput(coin string, entry, sl, tp float64, cs15, cs4 []exchange.Candle) string {
	n := len(cs15)
	price := cs15[n-1].Close

	// --- 15m 衍生(重用既有 indicator helper)---
	atr := robustATR(cs15, 14, 2)
	atrPct := 0.0
	extFromLowATR := 0.0
	if atr > 0 {
		atrPct = atr / price * 100
		low20 := cs15[n-1].Low
		for i := n - 20; i < n; i++ {
			if i >= 0 && cs15[i].Low < low20 {
				low20 = cs15[i].Low
			}
		}
		extFromLowATR = (price - low20) / atr
	}
	base := trimmedBaseVol(cs15)
	entryVolX, freshVolX := 0.0, 0.0
	if base > 0 {
		entryVolX = cs15[n-1].Volume / base
		for i := n - 6; i < n; i++ {
			if i >= 0 {
				if x := cs15[i].Volume / base; x > freshVolX {
					freshVolX = x
				}
			}
		}
	}
	chg24h := 0.0
	if n >= 97 && cs15[n-97].Close > 0 { // 96 根 15m = 24h
		chg24h = (price - cs15[n-97].Close) / cs15[n-97].Close * 100
	}

	// --- 4h 趨勢 + 上方壓力(重用 emaSeries)---
	m := len(cs4)
	e5 := emaSeries(cs4, 5)
	e20 := emaSeries(cs4, 20)
	e50 := emaSeries(cs4, 50)
	c4 := cs4[m-1].Close
	trend4h := "中性"
	switch {
	case e5[m-1] > e20[m-1] && c4 > e50[m-1]:
		trend4h = "上升"
	case e5[m-1] < e20[m-1] && c4 < e50[m-1]:
		trend4h = "下降"
	}
	distE50 := 0.0
	if e50[m-1] > 0 {
		distE50 = (c4 - e50[m-1]) / e50[m-1] * 100
	}
	// 上方最近的 4h 擺動高 = 壓力。找不到(突破進真空)= 上方無壓,對目標達成有利。
	nearestRes := 0.0
	for i := 2; i < m-2; i++ {
		h := cs4[i].High
		if h > cs4[i-1].High && h > cs4[i-2].High && h > cs4[i+1].High && h > cs4[i+2].High && h > entry*1.002 {
			if nearestRes == 0 || h < nearestRes {
				nearestRes = h
			}
		}
	}
	upsideRoomPct := -1.0 // -1 = 上方無明顯壓力
	fitsUnderRes := true
	if nearestRes > 0 {
		upsideRoomPct = (nearestRes - entry) / entry * 100
		fitsUnderRes = tp <= nearestRes
	}

	slDistPct := 0.0
	if entry > 0 {
		slDistPct = (sl - entry) / entry * 100
	}

	payload := map[string]any{
		"coin": coin, "dir": "long",
		"entry": entry, "sl": sl, "sl_dist_pct": round2(slDistPct),
		"target_R": 5, "target_price": tp,

		"atr_pct": round2(atrPct), "entry_vol_x": round2(entryVolX),
		"fresh_vol_x": round2(freshVolX), "ext_from_low_atr": round2(extFromLowATR),
		"chg_24h_pct": round2(chg24h),

		"btc_bias": biasZH(s.coinBias("BTC")), "eth_bias": biasZH(s.coinBias("ETH")),
		"funding_pct": round2(s.Funding(coin) * 100),

		"trend_4h": trend4h, "dist_ema50_4h_pct": round2(distE50),
		"upside_room_pct": round2(upsideRoomPct), "nearest_res": nearestRes,
		"target_fits_under_res": fitsUnderRes,
	}
	b, _ := json.Marshal(payload)
	return "這筆脈衝星v3(15m爆量、只做多)進場的即時數據:\n" + string(b) +
		"\n\n請依上面的錨定規則給參考信心分數。upside_room_pct 為 -1 代表上方無明顯壓力(對目標達成有利)。"
}

// biasZH maps the internal long/short/neutral bias to the 中文 labels the prompt uses.
func biasZH(b string) string {
	switch b {
	case "long":
		return "看漲"
	case "short":
		return "看跌"
	default:
		return "中性"
	}
}

// parsePulsarAI 從 AI 回覆抽第一段 {...} 解析,分數夾到 1–100、理由最多 4 條。
func parsePulsarAI(raw string) *pulsarAI {
	i := strings.IndexByte(raw, '{')
	j := strings.LastIndexByte(raw, '}')
	if i < 0 || j <= i {
		return nil
	}
	var c pulsarAI
	if err := json.Unmarshal([]byte(raw[i:j+1]), &c); err != nil {
		return nil
	}
	if c.Score < 1 || c.Score > 100 {
		return nil // 分數不在範圍 = 沒真的照規則答,寧可不顯示
	}
	if len(c.Reasons) > 4 {
		c.Reasons = c.Reasons[:4]
	}
	if len(c.Tag) > 24 {
		c.Tag = c.Tag[:24]
	}
	return &c
}
