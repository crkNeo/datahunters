package cache

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"datahunter/internal/hyperliquid"
)

// whales.go —— 名人動向(whale watch)。精選地址在 Hyperliquid 的即時倉位,
// 並用「快照 diff」產生動作事件流(開/加/減/平/反手)。資料 100% 鏈上公開、免費。
// ⚠️ 地址為社群/鏈上偵測標註,可能過期或標錯;純資訊呈現,非投資建議、跟單風險自負。

// Whale 是一個追蹤對象。Addr 為其 Hyperliquid 地址(小寫)。
type Whale struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	Note string `json:"note"` // 來源/身分註記
}

// whaleList 是預設精選名單。地址已由多個鏈上來源(Lookonchain/HypurrScan/Etherscan)交叉確認。
// 之後可改為後台可編輯;先用程式碼種子。
var whaleList = []Whale{
	{Name: "麻吉大哥", Addr: "0x020ca66c30bec2c4fe3861a94e4db4a498a35872", Note: "黃立成 · 台灣 · 常年重壓 ETH 多"},
	{Name: "James Wynn", Addr: "0x5078c2fbea2b2ad61bc840bc023e35fce56bedb6", Note: "@JamesWynnReal · 高槓桿迷因賭徒"},
	{Name: "AguilaTrades", Addr: "0x1f250df59a777d61cb8bd043c12970f3afe4f925", Note: "@AguilaTrades · BTC 大多頭巨鯨"},
	{Name: "Andrew Kang", Addr: "0xbb876071a63bc4d9bfcf46b012b4437ea7ff4281", Note: "Mechanism Capital 共同創辦人"},
	{Name: "Andrew Tate", Addr: "0xb78d97390a96a17fd2b58fedbeb3dd876c8f660a", Note: "@Cobratate · 網紅拳手"},
	{Name: "0xSifu", Addr: "0xf967239debef10dbc78e9bbbb2d8a16b72a614eb", Note: "Wonderland 前財務長 · 爭議人物"},
}

// knownLabels：地址 → 已知身分(小寫地址)。排行榜命中就顯示名字,否則顯示縮寫地址。
// 來源:HL 已知錢包目錄(cryexc)+ Lookonchain,含名單本身。⚠️ 社群標註,可能過期。
var knownLabels = map[string]string{
	"0x020ca66c30bec2c4fe3861a94e4db4a498a35872": "麻吉大哥",
	"0x5078c2fbea2b2ad61bc840bc023e35fce56bedb6": "James Wynn",
	"0x1f250df59a777d61cb8bd043c12970f3afe4f925": "AguilaTrades",
	"0xbb876071a63bc4d9bfcf46b012b4437ea7ff4281": "Andrew Kang",
	"0xb78d97390a96a17fd2b58fedbeb3dd876c8f660a": "Andrew Tate",
	"0xf967239debef10dbc78e9bbbb2d8a16b72a614eb": "0xSifu",
	"0x2639dc3ab1bc1eb232720be305ce83b57c14405b": "Ansem",
	"0xff0ad2ff560a84474c818521c6aedccbdce24fbf": "Pentoshi",
	"0xbcd420d13362532756c968f663f96ba95e240dd2": "0xTiamat",
	"0x741a58844ac349d6195d0b8ccf7fa07501eba5c2": "Colasama",
}

// labelOf 回傳地址的顯示名:已知則名字,否則 0x1234…abcd 縮寫。
func labelOf(addr string) string {
	if n, ok := knownLabels[strings.ToLower(addr)]; ok {
		return n
	}
	a := addr
	if len(a) > 12 {
		return a[:6] + "…" + a[len(a)-4:]
	}
	return a
}

// WhaleRank 是巨鯨排行的一列。
type WhaleRank struct {
	Rank    int                  `json:"rank"`
	Name    string               `json:"name"` // 已知名字或縮寫地址
	Addr    string               `json:"addr"`
	Known   bool                 `json:"known"` // 是否為已知身分
	Acct    float64              `json:"acct"`  // 帳戶淨值 USD
	Ntl     float64              `json:"ntl"`   // 總名目 USD
	NetLong bool                 `json:"net_long"`
	Top     hyperliquid.Position `json:"top"` // 最大單一持倉
}

// WhaleEvent 是一則動作事件。
type WhaleEvent struct {
	Time     int64   `json:"time"` // unix ms
	Name     string  `json:"name"`
	Addr     string  `json:"addr"`
	Kind     string  `json:"kind"` // open | add | reduce | close | flip
	Coin     string  `json:"coin"`
	Side     string  `json:"side"` // long | short(事件後的方向)
	Notional float64 `json:"notional"`
	Text     string  `json:"text"` // 中文描述
}

// WhaleCard 是單一對象的當前持倉卡。
type WhaleCard struct {
	Name      string                 `json:"name"`
	Addr      string                 `json:"addr"`
	Note      string                 `json:"note"`
	Acct      float64                `json:"acct"` // 帳戶淨值 USD
	Positions []hyperliquid.Position `json:"positions"`
}

// WhaleData 是 /api/whales 的回應。
type WhaleData struct {
	Cards     []WhaleCard  `json:"cards"`
	Events    []WhaleEvent `json:"events"`
	Rank      []WhaleRank  `json:"rank"` // 自動巨鯨排行(即時,依總名目)
	PushOn    bool         `json:"push_on"`
	UpdatedAt string       `json:"updated_at"`
	Source    string       `json:"source"`
}

// WhalePushEnabled 回傳名人動向推播是否開啟(後台可設定,預設關閉)。
func (s *Store) WhalePushEnabled() bool {
	if s.db == nil {
		return false
	}
	return s.db.getConfig("whale_push") == "1"
}

// SetWhalePush 設定名人動向推播開關(管理員)。
func (s *Store) SetWhalePush(on bool) {
	if s.db == nil {
		return
	}
	v := "0"
	if on {
		v = "1"
	}
	s.db.setConfig("whale_push", v)
}

// WhaleTick 抓每個追蹤對象的 Hyperliquid 倉位,與上次快照 diff 出動作事件,
// 並在推播開啟時推送重要事件(開倉/平倉/反手)。首抓只建立基準、不發事件。
func (s *Store) WhaleTick() {
	pushOn := s.WhalePushEnabled()
	var toPush []WhaleEvent
	for _, w := range whaleList {
		pos, acct, err := hyperliquid.FetchPositions(w.Addr)
		if err != nil {
			continue
		}
		cur := make(map[string]hyperliquid.Position, len(pos))
		for _, p := range pos {
			cur[p.Coin] = p
		}
		now := time.Now().UnixMilli()

		s.whaleMu.Lock()
		prev := s.whalePrev[w.Addr]
		seeded := s.whaleSeeded[w.Addr]
		var evs []WhaleEvent
		if seeded {
			evs = diffWhale(w, prev, cur, now)
		}
		s.whalePos[w.Addr] = pos
		s.whaleAcct[w.Addr] = acct
		s.whalePrev[w.Addr] = cur
		s.whaleSeeded[w.Addr] = true
		if len(evs) > 0 {
			s.whaleEvents = append(evs, s.whaleEvents...)
			if len(s.whaleEvents) > 120 {
				s.whaleEvents = s.whaleEvents[:120]
			}
		}
		s.whaleMu.Unlock()

		for _, e := range evs { // 只推重要事件,避免洗版
			if e.Kind == "open" || e.Kind == "close" || e.Kind == "flip" {
				toPush = append(toPush, e)
			}
		}
		time.Sleep(120 * time.Millisecond) // 禮貌地隔開兩次請求
	}
	s.whaleMu.Lock()
	s.whaleTime = time.Now()
	s.whaleMu.Unlock()

	if pushOn {
		for _, e := range toPush {
			s.PushSend("🐋 名人動向", e.Text, "/?tab=whales")
		}
	}
}

// diffWhale 比對前後快照,產生事件。用「size(基礎單位)」判斷加減倉,不受價格影響。
func diffWhale(w Whale, prev, cur map[string]hyperliquid.Position, now int64) []WhaleEvent {
	var out []WhaleEvent
	mk := func(kind, coin, side string, ntl float64, text string) {
		out = append(out, WhaleEvent{Time: now, Name: w.Name, Addr: w.Addr, Kind: kind, Coin: coin, Side: side, Notional: ntl, Text: text})
	}
	dirZh := func(s string) string {
		if s == "long" {
			return "多"
		}
		return "空"
	}
	for coin, c := range cur {
		p, had := prev[coin]
		if !had {
			mk("open", coin, c.Side, c.Notional, fmt.Sprintf("%s 開 %s %s $%s", w.Name, coin, dirZh(c.Side), humM(c.Notional)))
			continue
		}
		if p.Side != c.Side {
			mk("flip", coin, c.Side, c.Notional, fmt.Sprintf("%s 反手 %s %s→%s $%s", w.Name, coin, dirZh(p.Side), dirZh(c.Side), humM(c.Notional)))
			continue
		}
		if c.Size > p.Size*1.03 {
			mk("add", coin, c.Side, c.Notional, fmt.Sprintf("%s 加倉 %s %s → $%s", w.Name, coin, dirZh(c.Side), humM(c.Notional)))
		} else if c.Size < p.Size*0.97 {
			mk("reduce", coin, c.Side, c.Notional, fmt.Sprintf("%s 減倉 %s %s → $%s", w.Name, coin, dirZh(c.Side), humM(c.Notional)))
		}
	}
	for coin, p := range prev {
		if _, still := cur[coin]; !still {
			mk("close", coin, p.Side, p.Notional, fmt.Sprintf("%s 平倉 %s %s", w.Name, coin, dirZh(p.Side)))
		}
	}
	return out
}

// humM 把 USD 金額格式化為 M(百萬)。
func humM(v float64) string {
	if math.Abs(v) >= 1e6 {
		return fmt.Sprintf("%.1fM", v/1e6)
	}
	return fmt.Sprintf("%.0fK", v/1e3)
}

// WhaleBoard 組出 /api/whales 回應。
func (s *Store) WhaleBoard() WhaleData {
	s.whaleMu.RLock()
	defer s.whaleMu.RUnlock()
	out := WhaleData{Cards: []WhaleCard{}, Events: []WhaleEvent{}, PushOn: s.WhalePushEnabled(),
		Source: "Hyperliquid(鏈上公開)", UpdatedAt: s.whaleTime.Format(time.RFC3339)}
	for _, w := range whaleList {
		out.Cards = append(out.Cards, WhaleCard{
			Name: w.Name, Addr: w.Addr, Note: w.Note,
			Acct: s.whaleAcct[w.Addr], Positions: s.whalePos[w.Addr],
		})
	}
	out.Events = append(out.Events, s.whaleEvents...)
	out.Rank = append(out.Rank, s.whaleRank...)
	return out
}

// RefreshWhalePool 從 HL 排行榜取帳戶淨值前 N 大的地址,當作巨鯨排行的候選池。
// 排行榜檔案很大,失敗就保留舊池;偶爾刷新即可(每 6h)。
func (s *Store) RefreshWhalePool() {
	lb, err := hyperliquid.FetchLeaderboardTop(60)
	if err != nil || len(lb) == 0 {
		return
	}
	pool := make([]string, 0, len(lb))
	for _, e := range lb {
		pool = append(pool, e.Addr)
	}
	s.whaleMu.Lock()
	s.whalePool = pool
	s.whaleMu.Unlock()
}

// WhaleRankTick 抓候選池每個地址的即時倉位,依「總名目」排序,取前 20 名為巨鯨排行。
func (s *Store) WhaleRankTick() {
	s.whaleMu.RLock()
	pool := append([]string{}, s.whalePool...)
	s.whaleMu.RUnlock()
	if len(pool) == 0 {
		return // 尚未有候選池(等 RefreshWhalePool)
	}
	var rows []WhaleRank
	for _, a := range pool {
		pos, acct, err := hyperliquid.FetchPositions(a)
		if err != nil || len(pos) == 0 {
			continue
		}
		ntl, net := 0.0, 0.0
		var top hyperliquid.Position
		for _, p := range pos {
			ntl += math.Abs(p.Notional)
			if p.Side == "long" {
				net += p.Notional
			} else {
				net -= p.Notional
			}
			if math.Abs(p.Notional) > math.Abs(top.Notional) {
				top = p
			}
		}
		if ntl <= 0 {
			continue
		}
		name := labelOf(a)
		_, known := knownLabels[strings.ToLower(a)]
		rows = append(rows, WhaleRank{Name: name, Addr: a, Known: known, Acct: acct, Ntl: round2(ntl), NetLong: net >= 0, Top: top})
		time.Sleep(80 * time.Millisecond)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Ntl > rows[j].Ntl })
	if len(rows) > 20 {
		rows = rows[:20]
	}
	for i := range rows {
		rows[i].Rank = i + 1
	}
	s.whaleMu.Lock()
	s.whaleRank = rows
	s.whaleMu.Unlock()
}
