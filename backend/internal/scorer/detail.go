package scorer

import "fmt"

// DetailInput holds the per-coin metrics needed to build the detailed score
// card (the expandable per-coin view). All percentages are already in percent
// units (e.g. 1.7 means +1.7%), except FundingRate which is the raw rate.
type DetailInput struct {
	Coin        string
	OIChg1h     float64 // open interest 1h change, %
	CVDRatio    float64 // cumulative volume delta as % of volume
	StructLabel string  // "HH/HL" | "LH/LL" | "CHoCH" | "暫無明確 ..."
	StructDir   int     // +1 bullish, -1 bearish, 0 none
	Mom1h       float64 // 1h price change, %
	Mom24h      float64 // 24h price change, %
	FundingRate float64 // raw funding rate (e.g. 0.000013)
	LongAccount float64 // retail long account fraction 0..1
	RelStrength float64 // coin 24h% minus BTC 24h%
	Vol24h      float64 // trailing 24h quote (USDT) volume; 0 = unknown (no damping)
}

// BreakdownItem is one scored row in the 評分依據 table.
type BreakdownItem struct {
	Label string `json:"label"` // 市場結構
	Note  string `json:"note"`  // 主動做多 (OI 1H +0.1% / CVD 買壓)
	Score int    `json:"score"`
	Info  bool   `json:"info,omitempty"` // informational only — not added to total
}

// Rationale is one row in the 做多依據 / 做空依據 summary panel.
type Rationale struct {
	Label string `json:"label"` // 市場動能
	Tag   string `json:"tag"`   // 買方主導
	Tone  string `json:"tone"`  // pos | neg | neutral
	Text  string `json:"text"`  // 持倉量與 CVD 同步上升…
}

// DetailResult is the full computed score card for one coin.
type DetailResult struct {
	Coin      string          `json:"coin"`
	Total     int             `json:"total"`      // after liquidity damping
	Raw       int             `json:"raw"`        // sum of factor scores, pre-damping
	LiqFactor float64         `json:"liq_factor"` // 0..1 liquidity multiplier
	Rating    int             `json:"rating"`     // 0..10 dots
	Bias      string          `json:"bias"`       // long | short | neutral
	BiasLabel string          `json:"bias_label"`
	Rationale []Rationale     `json:"rationale"`
	Breakdown []BreakdownItem `json:"breakdown"`
}

// DetailWeights are the ceilings/half-saturation points for each factor.
// Kept in one place so the card can be retuned and backtested.
type DetailWeights struct {
	OIMax, OIHalf       float64
	CVDMax, CVDHalf     float64
	StructPts           float64 // points for a clean HH/HL or LH/LL
	Mom1hMax, Mom1hHalf float64
	Mom24hMax, Mom24hHalf float64
	FundingMax, FundingHalf float64
	CrowdMax, CrowdHalf float64
	RelMax, RelHalf     float64
	MinVol24h           float64 // 24h USDT volume below which the score is damped toward 0
	BiasThreshold       float64 // |total| to leave neutral
	RatingDivisor       float64 // total / divisor -> 0..10 dots
}

func DefaultDetailWeights() DetailWeights {
	return DetailWeights{
		OIMax: 15, OIHalf: 1.5, // 1.5 (was 0.3): backtest showed 0.3 over-reacted to 1h OI noise
		CVDMax: 15, CVDHalf: 8,
		StructPts: 8,
		Mom1hMax: 0, Mom1hHalf: 0.5, // disabled: reverse-indicator (see ScoreDetail)
		Mom24hMax: 10, Mom24hHalf: 2,
		FundingMax: 10, FundingHalf: 0.05,
		CrowdMax: 8, CrowdHalf: 15,
		RelMax: 0, RelHalf: 5, // disabled: reverse-indicator (see ScoreDetail)
		MinVol24h:     100e6, // backtest: thin (<100M) coins score far worse
		BiasThreshold: 20,
		RatingDivisor: 8,
	}
}

// ScoreDetail computes the full per-coin score card from raw metrics.
func ScoreDetail(in DetailInput, w DetailWeights) DetailResult {
	var items []BreakdownItem

	// 1) 市場結構: OI direction + CVD buy/sell pressure, the headline factor.
	oiPts := smoothSat(in.OIChg1h, w.OIMax, w.OIHalf)
	cvdPts := smoothSat(in.CVDRatio, w.CVDMax, w.CVDHalf)
	structScore := round(oiPts + cvdPts)
	items = append(items, BreakdownItem{
		Label: "市場結構",
		Note:  fmt.Sprintf("%s (OI 1H %+.1f%% / CVD %s)", marketStance(oiPts, cvdPts), in.OIChg1h, cvdSide(in.CVDRatio)),
		Score: structScore,
	})

	// 2) 價格結構 HH/HL/CHoCH
	priceStructScore := in.StructDir * round(w.StructPts)
	items = append(items, BreakdownItem{
		Label: "價格結構",
		Note:  in.StructLabel,
		Score: priceStructScore,
	})

	// 3) 動能 1H — informational only. Backtest (4h/12h/24h) showed 1h momentum
	// is consistently NEGATIVELY correlated with forward returns (it mean-reverts),
	// so it is shown for context but excluded from the score.
	items = append(items, BreakdownItem{
		Label: "動能 1H",
		Note:  fmt.Sprintf("%+.2f%%", in.Mom1h),
		Score: 0,
		Info:  true,
	})

	// 4) 動能 24H
	items = append(items, BreakdownItem{
		Label: "動能 24H",
		Note:  fmt.Sprintf("%+.2f%%", in.Mom24h),
		Score: round(smoothSat(in.Mom24h, w.Mom24hMax, w.Mom24hHalf)),
	})

	// 5) 資金費率: extreme funding is contrarian (positive funding leans short).
	frPct := in.FundingRate * 100
	items = append(items, BreakdownItem{
		Label: "資金費率",
		Note:  fmt.Sprintf("FR: %+.4f%%", frPct),
		Score: round(-smoothSat(frPct, w.FundingMax, w.FundingHalf)),
	})

	// 6) 多空比: fade crowded retail positioning.
	longPct := in.LongAccount * 100
	items = append(items, BreakdownItem{
		Label: "多空比",
		Note:  fmt.Sprintf("散戶%s %.1f%%", crowdSide(in.LongAccount), longPct),
		Score: round(-smoothSat((in.LongAccount-0.5)*100, w.CrowdMax, w.CrowdHalf)),
	})

	// 7) 相對強弱 vs BTC — informational only. Backtest showed coins that are
	// stronger than BTC tend to give it back (negatively correlated with forward
	// returns), so it is shown for context but excluded from the score.
	items = append(items, BreakdownItem{
		Label: "相對強弱",
		Note:  fmt.Sprintf("vs BTC: %+.1f%%", in.RelStrength),
		Score: 0,
		Info:  true,
	})

	raw := 0
	for _, it := range items {
		raw += it.Score
	}

	// liquidity damping: thin markets are pulled toward 0 (backtest-validated).
	// Unknown volume (0) is not penalised.
	liq := 1.0
	if w.MinVol24h > 0 && in.Vol24h > 0 && in.Vol24h < w.MinVol24h {
		liq = in.Vol24h / w.MinVol24h
	}
	total := round(float64(raw) * liq)

	rating := round(absF(float64(total)) / w.RatingDivisor)
	if rating > 10 {
		rating = 10
	}

	bias, biasLabel := "neutral", "觀察"
	switch {
	case float64(total) >= w.BiasThreshold:
		bias, biasLabel = "long", "做多"
	case float64(total) <= -w.BiasThreshold:
		bias, biasLabel = "short", "做空"
	}

	return DetailResult{
		Coin:      in.Coin,
		Total:     total,
		Raw:       raw,
		LiqFactor: liq,
		Rating:    rating,
		Bias:      bias,
		BiasLabel: biasLabel,
		Rationale: rationale(in, oiPts, cvdPts),
		Breakdown: items,
	}
}

// ScoreTotal returns just the numeric total of ScoreDetail (same arithmetic,
// no breakdown/strings) — a fast path for weight optimisation. Must stay in
// sync with ScoreDetail's per-item math.
func ScoreTotal(in DetailInput, w DetailWeights) int {
	raw := round(smoothSat(in.OIChg1h, w.OIMax, w.OIHalf) + smoothSat(in.CVDRatio, w.CVDMax, w.CVDHalf))
	raw += in.StructDir * round(w.StructPts)
	raw += round(smoothSat(in.Mom1h, w.Mom1hMax, w.Mom1hHalf))
	raw += round(smoothSat(in.Mom24h, w.Mom24hMax, w.Mom24hHalf))
	raw += round(-smoothSat(in.FundingRate*100, w.FundingMax, w.FundingHalf))
	raw += round(-smoothSat((in.LongAccount-0.5)*100, w.CrowdMax, w.CrowdHalf))
	raw += round(smoothSat(in.RelStrength, w.RelMax, w.RelHalf))
	liq := 1.0
	if w.MinVol24h > 0 && in.Vol24h > 0 && in.Vol24h < w.MinVol24h {
		liq = in.Vol24h / w.MinVol24h
	}
	return round(float64(raw) * liq)
}

// rationale builds the 4-row summary panel narrative.
func rationale(in DetailInput, oiPts, cvdPts float64) []Rationale {
	var out []Rationale

	// 市場動能 (OI + CVD)
	switch {
	case oiPts > 0 && cvdPts > 0:
		out = append(out, Rationale{"市場動能", "買方主導", "pos", "持倉量與 CVD 同步上升,多方持續進場"})
	case oiPts < 0 && cvdPts < 0:
		out = append(out, Rationale{"市場動能", "賣方主導", "neg", "持倉量與 CVD 同步下降,空方持續施壓"})
	case oiPts > 0 && cvdPts < 0:
		out = append(out, Rationale{"市場動能", "多空分歧", "neutral", "持倉增加但 CVD 偏賣,留意拉高出貨"})
	case oiPts < 0 && cvdPts > 0:
		out = append(out, Rationale{"市場動能", "多空分歧", "neutral", "持倉減少但 CVD 偏買,留意空頭回補"})
	default:
		out = append(out, Rationale{"市場動能", "中性", "neutral", "持倉與 CVD 無明顯方向"})
	}

	// 資金費率
	frPct := in.FundingRate * 100
	switch {
	case frPct >= 0.05:
		out = append(out, Rationale{"資金費率", "偏多擁擠", "neg", fmt.Sprintf("費率偏高 (%+.4f%%),多頭付費,留意多殺多", frPct)})
	case frPct <= -0.05:
		out = append(out, Rationale{"資金費率", "偏空擁擠", "pos", fmt.Sprintf("費率偏負 (%+.4f%%),空頭付費,留意軋空", frPct)})
	default:
		out = append(out, Rationale{"資金費率", "中性", "neutral", "費率接近零,市場情緒無明顯偏向"})
	}

	// 散戶情緒 (long/short)
	longPct := in.LongAccount * 100
	switch {
	case longPct >= 60:
		out = append(out, Rationale{"散戶情緒", "散戶偏多", "neg", fmt.Sprintf("散戶明顯偏多 (做多 %.0f%%),反向偏空", longPct)})
	case longPct <= 40:
		out = append(out, Rationale{"散戶情緒", "散戶偏空", "pos", fmt.Sprintf("散戶明顯偏空 (做多 %.0f%%),反向偏多", longPct)})
	default:
		out = append(out, Rationale{"散戶情緒", "中性", "neutral", fmt.Sprintf("多空情緒均衡 (做多 %.0f%%)", longPct)})
	}

	// 相對強弱
	switch {
	case in.RelStrength >= 0.5:
		out = append(out, Rationale{"相對強弱", "強於 BTC", "pos", fmt.Sprintf("相較 BTC 強 %.1f%%,幣種本身有相對強勢", in.RelStrength)})
	case in.RelStrength <= -0.5:
		out = append(out, Rationale{"相對強弱", "弱於 BTC", "neg", fmt.Sprintf("相較 BTC 弱 %.1f%%,幣種本身相對弱勢", -in.RelStrength)})
	default:
		out = append(out, Rationale{"相對強弱", "與 BTC 同步", "neutral", "走勢與 BTC 接近,無明顯相對強弱"})
	}

	return out
}

func marketStance(oiPts, cvdPts float64) string {
	sum := oiPts + cvdPts
	switch {
	case sum > 4:
		return "主動做多"
	case sum < -4:
		return "主動做空"
	default:
		return "區間整理"
	}
}

func cvdSide(cvd float64) string {
	switch {
	case cvd > 2:
		return "買壓"
	case cvd < -2:
		return "賣壓"
	default:
		return "中性"
	}
}

func crowdSide(longAccount float64) string {
	switch {
	case longAccount > 0.5:
		return "多翻"
	case longAccount < 0.5:
		return "空翻"
	default:
		return "持平"
	}
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
