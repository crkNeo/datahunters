// Package unlock builds the 代幣解鎖 (token-unlock) board from DefiLlama's free
// emissions datasets (no key). For each curated protocol it pulls the per-token
// emission schedule, computes the upcoming unlock (next 7d / 30d) as a share of
// circulating supply — the standard sell-pressure metric — plus the single
// largest unlock day in the next 30d (the "cliff" highlight). It then enriches
// symbol / price / circulating supply from CoinGecko in one batched call.
//
// Why windows instead of "next event": DefiLlama stores schedules at different
// granularities (some tokens are linearised to daily points, others keep monthly
// cliffs), so a raw "next data point" is not comparable across tokens. A fixed
// time-window aggregate is.
package unlock

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	datasetURL = "https://defillama-datasets.llama.fi/emissions/" // + slug
	geckoURL   = "https://api.coingecko.com/api/v3/coins/markets"
)

// Row is one token's upcoming-unlock summary.
type Row struct {
	Coin       string   `json:"coin"`         // ticker (from CoinGecko), upper-case
	Name       string   `json:"name"`         // protocol/token name
	Next7Amt   float64  `json:"next7_amt"`    // tokens unlocking in next 7d (non-staking)
	Next7Pct   float64  `json:"next7_pct"`    // % of circulating (or max if circ unknown)
	Next30Amt  float64  `json:"next30_amt"`   // tokens unlocking in next 30d
	Next30Pct  float64  `json:"next30_pct"`   // % of circulating (or max if circ unknown)
	ByCirc     bool     `json:"by_circ"`      // true: pct vs circulating; false: vs max supply
	USD30      float64  `json:"usd30"`        // next30 tokens × price (0 if no price)
	PeakDate   string   `json:"peak_date"`    // largest single unlock day within 30d (RFC3339)
	PeakAmt    float64  `json:"peak_amt"`     // tokens on that day
	PeakPctMax float64  `json:"peak_pct_max"` // % of max supply on that day
	Cats       []string `json:"cats"`         // allocation buckets unlocking in next 30d
	Price      float64  `json:"price"`        // USD (CoinGecko)
}

// Watcher fetches the unlock board.
type Watcher struct {
	http  *http.Client
	slugs []string
}

// DefaultSlugs is the curated set of DefiLlama emission protocols (majors + the
// high-attention unlock names). Fully-unlocked tokens simply drop out (no future
// unlock). All are verified to exist in DefiLlama's emissionsProtocolsList.
var DefaultSlugs = []string{
	// L1 / L2
	"aptos", "sui-foundation", "arbitrum", "optimism-foundation", "celestia", "sei",
	"near", "avalanche", "polkadot-treasury", "hedera", "injective-orderbook",
	"starknet-bridge", "zksync-era", "manta-pacific", "immutablex", "movement",
	"initia", "saga", "dymension", "altlayer", "berachain", "sonic", "zetachain",
	"flare", "metis", "mantle-bridge", "tron", "ton", "filecoin",
	// DeFi / infra
	"aave", "uniswap", "chainlink", "lido", "pendle", "gmx", "ethena", "ether.fi",
	"eigencloud", "morpho", "renzo", "jito", "jupiter", "pyth", "layerzero", "dydx",
	"ondo-finance", "the-graph", "usual", "grass", "bittensor", "worldcoin",
	"arkham", "portal", "aethir", "vana", "kaito", "moca-network", "ens",
	// GameFi / meme / NFT
	"apecoin", "axie-infinity", "official-trump", "beam", "big-time", "pudgy-penguins",
}

// groupZh maps DefiLlama's allocation-group keys to a short zh-TW bucket label.
func groupZh(group string) string {
	switch group {
	case "insiders":
		return "內部人"
	case "privateSale":
		return "私募"
	case "publicSale":
		return "公售"
	case "noncirculating":
		return "基金會/儲備"
	case "airdrop":
		return "空投"
	case "farming", "liquidityMining":
		return "流動性挖礦"
	case "staking":
		return "質押"
	default:
		return "" // unknown → caller keeps the raw label
	}
}

// NewWatcher builds a watcher over the default curated slug set.
func NewWatcher() *Watcher {
	return &Watcher{http: &http.Client{Timeout: 40 * time.Second}, slugs: DefaultSlugs}
}

// dataset is the subset of a per-protocol emissions file we need.
type dataset struct {
	Name       string              `json:"name"`
	Gecko      string              `json:"gecko_id"`
	Categories map[string][]string `json:"categories"`
	Supply     struct {
		Max float64 `json:"maxSupply"`
	} `json:"supplyMetrics"`
	Doc struct {
		Data []struct {
			Label string `json:"label"`
			Data  []struct {
				T int64   `json:"timestamp"`
				U float64 `json:"unlocked"`
			} `json:"data"`
		} `json:"data"`
	} `json:"documentedData"`
}

// parsed holds the pre-enrichment (no price) computation for one token.
type parsed struct {
	name, gecko          string
	next7, next30, max   float64
	peakAmt              float64
	peakTS               int64
	cats                 []string
}

// fetchOne pulls + parses one protocol. Returns nil if it has no unlock in the
// next 30 days (fully unlocked, or next cliff further out) — such tokens are not
// "upcoming" and are omitted from the board.
func (w *Watcher) fetchOne(slug string) *parsed {
	req, err := http.NewRequest("GET", datasetURL+slug, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil
	}
	var d dataset
	if json.Unmarshal(body, &d) != nil {
		return nil
	}
	skip := map[string]bool{} // staking = continuous emission, not a scheduled unlock
	for _, l := range d.Categories["staking"] {
		skip[l] = true
	}
	labelGroup := map[string]string{} // raw label → zh bucket name (投資人/內部人/…)
	for group, labels := range d.Categories {
		for _, l := range labels {
			labelGroup[l] = groupZh(group)
		}
	}
	now := time.Now().Unix()
	h7, h30 := now+7*86400, now+30*86400
	var n7, n30, peak float64
	var peakTS int64
	catSet := map[string]bool{}
	daily := map[int64]float64{} // date → total non-staking unlock (for the cliff highlight)
	for _, c := range d.Doc.Data {
		if skip[c.Label] {
			continue
		}
		var prev float64
		first := true
		for _, pt := range c.Data {
			if !first && pt.U > prev && pt.T > now && pt.T <= h30 {
				delta := pt.U - prev
				n30 += delta
				daily[pt.T] += delta
				if pt.T <= h7 {
					n7 += delta
				}
				if g := labelGroup[c.Label]; g != "" {
					catSet[g] = true
				} else {
					catSet[c.Label] = true // unknown group → keep raw label
				}
			}
			prev = pt.U
			first = false
		}
	}
	if n30 <= 0 {
		return nil
	}
	for t, v := range daily { // largest single unlock day within 30d
		if v > peak {
			peak, peakTS = v, t
		}
	}
	cats := make([]string, 0, len(catSet))
	for l := range catSet {
		cats = append(cats, l)
	}
	sort.Strings(cats)
	return &parsed{d.Name, d.Gecko, n7, n30, d.Supply.Max, peak, peakTS, cats}
}

// gecko is one CoinGecko /coins/markets entry (the fields we use).
type gecko struct {
	ID     string  `json:"id"`
	Symbol string  `json:"symbol"`
	Price  float64 `json:"current_price"`
	Circ   float64 `json:"circulating_supply"`
}

// enrich pulls symbol / price / circulating supply for the given gecko ids in one
// batched call. Best-effort: an empty map just means pct falls back to max supply.
func (w *Watcher) enrich(ids []string) map[string]gecko {
	out := map[string]gecko{}
	if len(ids) == 0 {
		return out
	}
	u := geckoURL + "?vs_currency=usd&per_page=250&ids=" + strings.Join(ids, ",")
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return out
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := w.http.Do(req)
	if err != nil {
		return out
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return out
	}
	var arr []gecko
	if json.Unmarshal(body, &arr) != nil {
		return out
	}
	for _, g := range arr {
		out[g.ID] = g
	}
	return out
}

// Fetch builds the full board: parse each curated protocol, enrich via CoinGecko,
// and return rows sorted by upcoming 30d unlock (biggest sell-pressure first).
// Returns ok=false only if every dataset fetch failed (so the caller can alert).
func (w *Watcher) Fetch() (rows []Row, ok bool) {
	var ps []*parsed
	var ids []string
	anyFetched := false
	for _, slug := range w.slugs {
		p := w.fetchOne(slug)
		anyFetched = true // fetchOne returning nil for "no upcoming unlock" is a success too
		if p == nil {
			continue
		}
		ps = append(ps, p)
		if p.gecko != "" {
			ids = append(ids, p.gecko)
		}
		time.Sleep(60 * time.Millisecond) // gentle pacing on the dataset CDN
	}
	if !anyFetched {
		return nil, false
	}
	mk := w.enrich(ids)
	for _, p := range ps {
		g := mk[p.gecko]
		denom, byCirc := g.Circ, true
		if denom <= 0 {
			denom, byCirc = p.max, false // fall back to max supply
		}
		pct := func(x float64) float64 {
			if denom <= 0 {
				return 0
			}
			return x / denom * 100
		}
		coin := strings.ToUpper(g.Symbol)
		if coin == "" {
			coin = p.name
		}
		peakDate := ""
		if p.peakTS > 0 {
			peakDate = time.Unix(p.peakTS, 0).UTC().Format(time.RFC3339)
		}
		peakPctMax := 0.0
		if p.max > 0 {
			peakPctMax = p.peakAmt / p.max * 100
		}
		rows = append(rows, Row{
			Coin: coin, Name: p.name,
			Next7Amt: p.next7, Next7Pct: pct(p.next7),
			Next30Amt: p.next30, Next30Pct: pct(p.next30), ByCirc: byCirc,
			USD30:      p.next30 * g.Price,
			PeakDate:   peakDate,
			PeakAmt:    p.peakAmt,
			PeakPctMax: peakPctMax,
			Cats:       p.cats,
			Price:      g.Price,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Next30Pct > rows[j].Next30Pct })
	return rows, true
}
