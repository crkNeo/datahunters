package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"datahunter/internal/exchange"
)

func TestPartialBar(t *testing.T) {
	const start = int64(1_700_000_100_000) - int64(1_700_000_100_000)%barMs
	ms := []exchange.Candle{
		{Ts: start - minMs, Open: 9, High: 99, Low: 1, Close: 9, Volume: 1000}, // 上一根 15m 的,要忽略
		{Ts: start, Open: 10, High: 11, Low: 9.5, Close: 10.5, Volume: 100, TakerBuy: 60, QuoteVol: 1000},
		{Ts: start + minMs, Open: 10.5, High: 12, Low: 10.2, Close: 11.8, Volume: 50, TakerBuy: 40, QuoteVol: 590},
	}
	p, ok := partialBar(ms, start)
	if !ok {
		t.Fatal("應拼出部分 K")
	}
	if p.Ts != start || p.Open != 10 || p.High != 12 || p.Low != 9.5 || p.Close != 11.8 || p.Volume != 150 || p.TakerBuy != 100 || p.QuoteVol != 1590 {
		t.Errorf("部分 K 錯: %+v", p)
	}
	if _, ok := partialBar(nil, start); ok {
		t.Error("沒有 1m K 不該成立")
	}
}

func TestRiskPct(t *testing.T) {
	if r := riskPct(100, 103); math.Abs(r-3) > 1e-9 {
		t.Errorf("riskPct=%v want 3", r)
	}
	if riskPct(100, 0) != 0 || riskPct(0, 3) != 0 {
		t.Error("缺資料應回 0")
	}
}

func TestReadCSVStripsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.csv")
	os.WriteFile(path, []byte("\xEF\xBB\xBFcoin,entry\nAERO,0.85\nBAD\n"), 0o644)
	h, rows, err := readCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if h[0] != "coin" {
		t.Errorf("BOM 沒去掉: %q", h[0])
	}
	if len(rows) != 1 || rows[0][0] != "AERO" {
		t.Errorf("rows=%v(欄數不符的列要略過)", rows)
	}
}

func TestPadKeepsColumnCount(t *testing.T) {
	if got := pad([]string{"1", "2"}, "err"); len(got) != len(newCols) || got[len(got)-1] != "err" {
		t.Errorf("pad 欄數 %d, want %d", len(got), len(newCols))
	}
}
