package cache

import (
	"sort"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

// funding.go: a standalone public 資金費率 board sourced from OKX (spreads load off
// Binance). Independent of fundMap (the Binance/WS funding used in the scorer). It
// covers the top-N USDT swaps by 24h volume, tagged by sector (板塊).

const fundingTopN = 300

// FundingRow is one coin's current funding rate (from OKX).
type FundingRow struct {
	Coin   string  `json:"coin"`
	Rate   float64 `json:"rate"`    // per-interval funding rate (fraction, e.g. 0.0001 = 0.01%)
	Sector string  `json:"sector"`  // 板塊 (best-effort; 其他 if unknown)
	NextMs int64   `json:"next_ms"` // next funding time (unix ms)
}

// FundingData is the funding-tab payload.
type FundingData struct {
	Rows      []FundingRow `json:"rows"`
	UpdatedAt string       `json:"updated_at"`
}

// FundingTick ranks OKX USDT swaps by 24h volume, keeps the top N, and pulls each
// one's funding rate (OKX has no bulk funding endpoint → one call per coin, paced).
// Funding moves slowly, so this runs on a slow ticker.
func (s *Store) FundingTick() {
	tickers, err := s.ex.OKXSwapTickers()
	if err != nil || len(tickers) == 0 {
		s.apiFail("OKX 資金費率", "取合約清單失敗")
		return
	}
	var list []exchange.OKXTicker
	for _, t := range tickers {
		if strings.HasSuffix(t.InstID, "-USDT-SWAP") {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].VolCcy24h > list[j].VolCcy24h })
	if len(list) > fundingTopN {
		list = list[:fundingTopN]
	}
	rows := make([]FundingRow, 0, len(list))
	for _, t := range list {
		rate, next, err := s.ex.OKXFundingInfo(t.InstID)
		if err != nil {
			continue
		}
		coin := strings.TrimSuffix(t.InstID, "-USDT-SWAP")
		rows = append(rows, FundingRow{Coin: coin, Rate: rate, Sector: fundSector(coin), NextMs: next})
		time.Sleep(110 * time.Millisecond) // pace OKX (funding-rate endpoint ~10 req/s cap)
	}
	if len(rows) == 0 {
		s.apiFail("OKX 資金費率", "全部費率抓取失敗")
		return
	}
	s.apiOK("OKX 資金費率")
	sort.Slice(rows, func(i, j int) bool { return rows[i].Rate > rows[j].Rate })
	s.fundBoardMu.Lock()
	s.fundBoard = rows
	s.fundBoardTime = time.Now()
	s.fundBoardMu.Unlock()
}

// FundingBoard returns the cached OKX funding board (most-positive first).
func (s *Store) FundingBoard() FundingData {
	s.fundBoardMu.RLock()
	defer s.fundBoardMu.RUnlock()
	rows := make([]FundingRow, len(s.fundBoard))
	copy(rows, s.fundBoard)
	out := FundingData{Rows: rows}
	if !s.fundBoardTime.IsZero() {
		out.UpdatedAt = s.fundBoardTime.Format(time.RFC3339)
	}
	return out
}

// ---- sector (板塊) map: best-effort, built from groups so a duplicate coin just
// takes the last-listed sector (no compile error), unknown coins → 其他. ----

var sectorGroups = []struct {
	sector string
	coins  []string
}{
	{"L1公鏈", []string{"BTC", "ETH", "SOL", "BNB", "ADA", "AVAX", "TRX", "DOT", "NEAR", "APT", "SUI", "ATOM", "TON", "ICP", "SEI", "TIA", "KAS", "ALGO", "HBAR", "EGLD", "FLOW", "MINA", "KAVA", "XTZ", "ZIL", "ONE", "CELO", "QTUM", "IOTA", "XLM", "XRP", "LTC", "BCH", "ETC", "MOVE", "BERA", "S", "HYPE", "APEX", "CORE", "KDA", "NEO", "WAVES", "AXL"}},
	{"L2擴容", []string{"ARB", "OP", "MATIC", "POL", "STRK", "METIS", "MANTA", "ZK", "BLAST", "TAIKO", "LRC", "SKL", "CYBER"}},
	{"DeFi", []string{"UNI", "AAVE", "MKR", "CRV", "LDO", "SNX", "COMP", "SUSHI", "DYDX", "GMX", "PENDLE", "ENA", "JUP", "RUNE", "CAKE", "1INCH", "BAL", "YFI", "FXS", "ENS", "MORPHO", "AERO", "RAY", "JTO", "ETHFI", "EIGEN", "DRIFT", "OSMO", "SPELL", "HFT"}},
	{"Meme", []string{"DOGE", "SHIB", "PEPE", "WIF", "BONK", "FLOKI", "TRUMP", "BOME", "MEME", "POPCAT", "MEW", "BRETT", "PNUT", "GOAT", "ACT", "NEIRO", "MOODENG", "DEGEN", "TURBO", "PONKE", "CHILLGUY", "FARTCOIN", "SPX", "MOG", "BABYDOGE", "SLERF", "MICHI", "GIGA"}},
	{"AI", []string{"FET", "RENDER", "RNDR", "WLD", "TAO", "AGIX", "OCEAN", "ARKM", "AIOZ", "PHB", "IO", "AI16Z", "VIRTUAL", "ZEREBRO", "GRIFFAIN", "AIXBT", "NMR", "CGPT"}},
	{"DePIN", []string{"HNT", "IOTX", "JASMY", "THETA", "GRASS", "AKT", "DIMO", "NATIX", "RENDER"}},
	{"存儲", []string{"FIL", "AR", "STORJ", "BTT", "SC"}},
	{"GameFi", []string{"SAND", "MANA", "AXS", "GALA", "APE", "ENJ", "PIXEL", "BEAM", "GMT", "MAGIC", "PORTAL", "ACE", "YGG", "BIGTIME", "PRIME", "NAKA", "CATI"}},
	{"交易所", []string{"OKB", "CRO", "BGB", "GT", "KCS", "WOO", "HT", "MX"}},
	{"Oracle", []string{"LINK", "PYTH", "BAND", "API3", "TRB"}},
	{"隱私", []string{"XMR", "ZEC", "DASH", "ROSE", "SCRT"}},
	{"RWA", []string{"ONDO", "OM", "PENDLE", "POLYX", "CFG", "TOKEN"}},
}

var coinSector = func() map[string]string {
	m := map[string]string{}
	for _, g := range sectorGroups {
		for _, c := range g.coins {
			m[c] = g.sector
		}
	}
	return m
}()

// fundSector returns the funding board's richer sector label (separate from the
// detail view's sectorOf, which covers only the core coin universe).
func fundSector(coin string) string {
	if s, ok := coinSector[coin]; ok {
		return s
	}
	return "其他"
}
