// Command pulsartag 替已匯出的脈衝星成交補上「進場當下」的雷達/評分器因子,輸出新的 CSV 供分組分析。
//
// 用法(在連得到 Binance 的機器上跑;OI / 多空比歷史只保留約 30 天,更早的單那兩欄會是空的):
//
//	go run ./cmd/pulsartag -out tagged.csv pulsarv12-xxxx.csv pulsarv3-xxxx.csv
//
// 輸入:後台「⬇ 匯出 CSV」下載的檔(/api/admin/export?book=pulsarv12 …),可一次給多個。
// 輸出:原本所有欄位 + 下面這些(t_ 開頭的口徑與線上 cache.ComputeEntryTags 完全一致):
//
//	r_pct      初始風險% = |TP1 − 進場| / 進場(脈衝星 TP1 = 1R)
//	pnl_r      損益換算成 R 倍數 = pnl_pct / r_pct
//	intrabar   1 = 盤中進場(進場時間不在 15m 整點上)
//	v3_confirm 盤中單才有:那根 15m 收完後,v3 的進場條件是否也成立(1/0)
//	t_oi12h t_oi1h t_cvd6h t_cvd1h t_funding t_long_acct t_chg24 t_surge_x t_btc1h t_vol24
//	tag_err    抓資料失敗的項目(空 = 全部成功)
//
// 防偷看未來:所有資料都只取進場時刻之前的。盤中進場那根 15m 用 1m K 拼出「進場前」的部分 K,
// 不直接用交易所事後的完整 K(v3_confirm 例外,它本來就是在問收盤後的結果)。
package main

import (
	"encoding/csv"
	"flag"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"datahunter/internal/cache"
	"datahunter/internal/exchange"
)

const (
	minMs  = 60_000
	barMs  = 15 * minMs
	hourMs = 60 * minMs
)

var newCols = []string{"r_pct", "pnl_r", "intrabar", "v3_confirm",
	"t_oi12h", "t_oi1h", "t_cvd6h", "t_cvd1h", "t_funding", "t_long_acct",
	"t_chg24", "t_surge_x", "t_btc1h", "t_vol24", "tag_err"}

func main() {
	out := flag.String("out", "pulsar-tagged.csv", "輸出 CSV 路徑")
	sleep := flag.Duration("sleep", 300*time.Millisecond, "每個 Binance 請求之間的間隔(避免被限流)")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("請給至少一個匯出的 CSV,例如: go run ./cmd/pulsartag -out tagged.csv pulsarv12.csv")
	}

	var header []string
	var rows [][]string
	for _, path := range flag.Args() {
		h, rs, err := readCSV(path)
		if err != nil {
			log.Fatalf("讀取 %s 失敗: %v", path, err)
		}
		if header == nil {
			header = h
		} else if strings.Join(h, ",") != strings.Join(header, ",") {
			log.Fatalf("%s 的欄位跟第一個檔不同,請用同一版後台匯出", path)
		}
		rows = append(rows, rs...)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"coin", "entry", "pnl_pct", "open_time", "tp1"} {
		if _, ok := col[need]; !ok {
			log.Fatalf("CSV 缺少欄位 %q —— 請用新版後台的「匯出 CSV」", need)
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	f.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
	w := csv.NewWriter(f)
	w.Write(append(append([]string{}, header...), newCols...))

	t := &tagger{ex: exchange.NewClient(), sleep: *sleep, btcPrevHour: map[int64]float64{}}
	for i, r := range rows {
		get := func(k string) string { return r[col[k]] }
		w.Write(append(append([]string{}, r...), t.tagRow(get)...))
		w.Flush()
		log.Printf("[%d/%d] %s %s", i+1, len(rows), get("coin"), get("open_time"))
	}
	log.Printf("完成,輸出 %s(%d 筆)", *out, len(rows))
}

func readCSV(path string) ([]string, [][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	h, err := r.Read()
	if err != nil {
		return nil, nil, err
	}
	if len(h) > 0 {
		h[0] = strings.TrimPrefix(h[0], string(rune(0xFEFF))) // 匯出檔開頭的 BOM
	}
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if len(rec) == len(h) {
			rows = append(rows, rec)
		}
	}
	return h, rows, nil
}

type tagger struct {
	ex          *exchange.Client
	sleep       time.Duration
	btcPrevHour map[int64]float64 // 整點 → 上一個整點 BTC 收盤(同一小時的單共用)
}

func (t *tagger) pause() { time.Sleep(t.sleep) }

// tagRow 回傳一筆單的新欄位(順序同 newCols)。
func (t *tagger) tagRow(get func(string) string) []string {
	entry := atof(get("entry"))
	pnl := atof(get("pnl_pct"))
	rPct := riskPct(entry, atof(get("tp1")))
	pnlR := ""
	if rPct > 0 {
		pnlR = ff(pnl / rPct)
	}
	ot, err := time.Parse(time.RFC3339, get("open_time"))
	if err != nil {
		return pad([]string{ff(rPct), pnlR}, "open_time 格式錯誤")
	}
	entryMs := ot.UnixMilli()
	sym := get("coin") + "USDT"
	barStart := entryMs - entryMs%barMs
	minuteEnd := entryMs - entryMs%minMs - 1 // 進場那一分鐘之前(不含還沒走完的那根 1m)
	intrabar := entryMs != barStart
	var errs []string

	// 15m K:已收的棒 + 盤中單的「進場前部分 K」
	cs, err := t.ex.BinanceKlinesRange(sym, "15m", 0, barStart-1, 200)
	t.pause()
	if err != nil {
		errs = append(errs, "15m")
	}
	if intrabar && minuteEnd >= barStart {
		ms, err := t.ex.BinanceKlinesRange(sym, "1m", barStart, minuteEnd, 15)
		t.pause()
		if err != nil {
			errs = append(errs, "1m部分K")
		} else if p, ok := partialBar(ms, barStart); ok {
			cs = append(cs, p)
		}
	}

	v3c := ""
	if intrabar {
		full, err := t.ex.BinanceKlinesRange(sym, "15m", 0, barStart+barMs-1, 200)
		t.pause()
		if err != nil || len(full) == 0 || full[len(full)-1].Ts != barStart {
			errs = append(errs, "v3確認")
		} else if cache.SurgeV3OK(full) {
			v3c = "1"
		} else {
			v3c = "0"
		}
	}

	oi, err := t.ex.BinanceOIHistAt(sym, "1h", entryMs, 13)
	t.pause()
	if err != nil || len(oi) < 2 {
		errs = append(errs, "OI(超過30天?)")
	}
	ls, err := t.ex.BinanceLongShortAt(sym, "5m", entryMs)
	t.pause()
	if err != nil {
		errs = append(errs, "多空比(超過30天?)")
	}
	fr, err := t.ex.BinanceFundingAt(sym, entryMs)
	t.pause()
	if err != nil {
		errs = append(errs, "費率")
	}
	btc, ok := t.btc1h(entryMs, minuteEnd)
	if !ok {
		errs = append(errs, "BTC")
	}
	surgeX := t.surgeX(sym, minuteEnd)
	if surgeX == 0 {
		errs = append(errs, "爆量倍數")
	}

	tags := cache.ComputeEntryTags(cs, oi, fr.Rate, ls.LongAccount, btc, surgeX)
	ib := "0"
	if intrabar {
		ib = "1"
	}
	return []string{ff(rPct), pnlR, ib, v3c,
		ff(tags.OI12h), ff(tags.OI1h), ff(tags.CVD6h), ff(tags.CVD1h), ff(tags.Funding), ff(tags.LongAcct),
		ff(tags.Chg24), ff(tags.SurgeX), ff(tags.BTC1h), ff(tags.Vol24), strings.Join(errs, "|")}
}

// btc1h = BTC 進場前最後一根 1m 收盤,對上一個整點收盤的漲跌%(與線上 btc1hRet 同口徑)。
func (t *tagger) btc1h(entryMs, minuteEnd int64) (float64, bool) {
	hourStart := entryMs - entryMs%hourMs
	prev, ok := t.btcPrevHour[hourStart]
	if !ok {
		kl, err := t.ex.BinanceKlinesRange("BTCUSDT", "1h", 0, hourStart-1, 1)
		t.pause()
		if err != nil || len(kl) == 0 || kl[0].Close <= 0 {
			return 0, false
		}
		prev = kl[0].Close
		t.btcPrevHour[hourStart] = prev
	}
	kl, err := t.ex.BinanceKlinesRange("BTCUSDT", "1m", 0, minuteEnd, 1)
	t.pause()
	if err != nil || len(kl) == 0 {
		return 0, false
	}
	return (kl[0].Close/prev - 1) * 100, true
}

// surgeX 抓進場前約 2000 根 1m K(24h 滾動量 + 8h 快照環),重建當時的爆量倍數。
func (t *tagger) surgeX(sym string, minuteEnd int64) float64 {
	late, err := t.ex.BinanceKlinesRange(sym, "1m", 0, minuteEnd, 1500)
	t.pause()
	if err != nil || len(late) == 0 {
		return 0
	}
	early, err := t.ex.BinanceKlinesRange(sym, "1m", 0, late[0].Ts-1, 500)
	t.pause()
	if err != nil {
		early = nil
	}
	return cache.SurgeXFromMinuteKlines(append(early, late...))
}

// partialBar 把 [barStart, 進場) 的 1m K 拼成一根 15m 部分 K(開=第一根開、收=最後一根收)。
func partialBar(ms []exchange.Candle, barStart int64) (exchange.Candle, bool) {
	p := exchange.Candle{Ts: barStart}
	n := 0
	for _, m := range ms {
		if m.Ts < barStart || m.Ts >= barStart+barMs {
			continue
		}
		if n == 0 {
			p.Open, p.High, p.Low = m.Open, m.High, m.Low
		}
		p.High = math.Max(p.High, m.High)
		p.Low = math.Min(p.Low, m.Low)
		p.Close = m.Close
		p.Volume += m.Volume
		p.TakerBuy += m.TakerBuy
		p.QuoteVol += m.QuoteVol
		p.TakerBuyQuote += m.TakerBuyQuote
		p.Trades += m.Trades
		n++
	}
	return p, n > 0
}

// riskPct = 初始風險% = |TP1 − 進場| / 進場 × 100(脈衝星的 TP1 固定在 1R)。沒有 TP1 回 0。
func riskPct(entry, tp1 float64) float64 {
	if entry <= 0 || tp1 <= 0 {
		return 0
	}
	return math.Abs(tp1-entry) / entry * 100
}

// pad 在前段失敗時補齊欄數,最後一欄放錯誤訊息。
func pad(head []string, msg string) []string {
	out := append([]string{}, head...)
	for len(out) < len(newCols)-1 {
		out = append(out, "")
	}
	return append(out, msg)
}

func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

func ff(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', 6, 64)
}
