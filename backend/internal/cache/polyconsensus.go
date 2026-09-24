package cache

// polyconsensus.go: 跟單篩選 · 聰明錢共識。每小時把 Polymarket CRYPTO 排行榜前 20 名
// 交易者「目前的持倉」丟給免費 AI(重用大盤分析的 marketai/Groq),請它判讀「每個人現在
// 覺得市場怎麼走」+ 彙整整體大綱與信心。刻意「一次打包」20 人成 1 次請求,免費層額度綽綽有餘。
//
// 為什麼不看進場價:等看到倉,現價常已貼到 1,跟進沒空間;倉位方向+部位大小才是他們
// 「現在的看法」。所以這裡只用方向與部位,不談進場。純資訊,非投資建議。

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"datahunter/internal/polymarket"
)

// PolyView 是單一交易者的「目前看法」(AI 判讀 + 佐證倉位)。
type PolyView struct {
	Wallet    string                `json:"wallet"`
	Name      string                `json:"name"`
	Verdict   string                `json:"verdict"` // 一致性判定(沿用 Screen:長期穩定/短期爆發…)
	Lean      string                `json:"lean"`    // AI 判的方向:偏多/偏空/中性/區間
	View      string                `json:"view"`    // AI 一句話看法
	AllPnL    float64               `json:"all_pnl"`
	MonthPnL  float64               `json:"month_pnl"`
	Positions []polymarket.Position `json:"positions"` // 佐證(前端可展開;不顯示進場)
}

// PolyConsensus 是一份完整彙整:整體大綱 + 前 20 名個別看法。
type PolyConsensus struct {
	Summary    string     `json:"summary"`    // 整體大綱(2-3 句)
	Lean       string     `json:"lean"`       // 整體方向
	Confidence string     `json:"confidence"` // 整體信心:高/中/低
	Views      []PolyView `json:"views"`      // 前 20 名個別
	UpdatedAt  string     `json:"updated_at"`
	Source     string     `json:"source"` // AI provider
	PushOn     bool       `json:"push_on"`
}

const polySystem = "你是加密貨幣預測市場(Polymarket)分析師。我會給你排行榜前 N 名交易者目前的持倉," +
	"每筆是「市場問題 + 他押的方向(Yes/No/Up/Down)+ 部位美元」。押 No 常代表賭某事不會發生。\n" +
	"請只根據這些倉位判讀:(1) 每個人目前認為市場會怎麼走;(2) 全體的整體共識與信心。" +
	"不要編造沒有的市場或數字。用繁體中文、精簡有觀點。\n" +
	"嚴格『只輸出 JSON』(不要 markdown、不要圍欄、不要多餘文字),格式:\n" +
	`{"overall":{"lean":"偏多|偏空|中性|區間","confidence":"高|中|低","summary":"2-3句整體大綱"},` +
	`"views":[{"i":序號,"lean":"偏多|偏空|中性|區間","view":"這個人目前的看法,一句話"}]}` + "\n" +
	"views 必須涵蓋我列出的每一個序號;無持倉者 view 寫「目前無公開持倉」、lean 寫「中性」。"

// buildPolyUser 把前 20 名的倉位排成精簡文字(每人最多 6 大倉,控制 token)。
func buildPolyUser(reps []polymarket.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "以下是 Polymarket CRYPTO 排行榜前 %d 名交易者目前的持倉:\n\n", len(reps))
	for i, r := range reps {
		name := r.Name
		if name == "" && len(r.Wallet) >= 10 {
			name = r.Wallet[:10]
		}
		fmt.Fprintf(&b, "%d. %s(一致性:%s)\n", i+1, name, r.Verdict)
		if len(r.Positions) == 0 {
			b.WriteString("   - 目前無公開持倉\n")
			continue
		}
		n := len(r.Positions)
		if n > 6 {
			n = 6
		}
		for _, p := range r.Positions[:n] {
			fmt.Fprintf(&b, "   - %s｜押 %s｜$%.0f\n", p.Title, p.Outcome, p.CurValue)
		}
	}
	return b.String()
}

// extractJSON 從 AI 回覆裡挖出 JSON 主體(取第一個 { 到最後一個 }),
// 容忍前後多餘文字或 ```json 圍欄。
func extractJSON(s string) string {
	a := strings.IndexByte(s, '{')
	b := strings.LastIndexByte(s, '}')
	if a >= 0 && b > a {
		return s[a : b+1]
	}
	return s
}

// parsePolyAI 解析 AI 的 JSON 並對回 reps(依序號 i,1-based),產出完整彙整。
// 純函式、不發網路,方便測 —— AI 漏掉的個別看法留白,不影響其他資料。
func parsePolyAI(raw string, reps []polymarket.Report) (PolyConsensus, error) {
	var out struct {
		Overall struct {
			Lean       string `json:"lean"`
			Confidence string `json:"confidence"`
			Summary    string `json:"summary"`
		} `json:"overall"`
		Views []struct {
			I    int    `json:"i"`
			Lean string `json:"lean"`
			View string `json:"view"`
		} `json:"views"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return PolyConsensus{}, fmt.Errorf("poly-AI JSON 解析失敗: %w", err)
	}
	type lv struct{ lean, view string }
	byIdx := make(map[int]lv, len(out.Views))
	for _, v := range out.Views {
		byIdx[v.I] = lv{v.Lean, v.View}
	}
	views := make([]PolyView, 0, len(reps))
	for i, r := range reps {
		pv := PolyView{
			Wallet: r.Wallet, Name: r.Name, Verdict: r.Verdict,
			AllPnL: r.AllPnL, MonthPnL: r.MonthPnL, Positions: r.Positions,
		}
		if a, ok := byIdx[i+1]; ok {
			pv.Lean, pv.View = a.lean, a.view
		}
		views = append(views, pv)
	}
	return PolyConsensus{
		Summary: out.Overall.Summary, Lean: out.Overall.Lean,
		Confidence: out.Overall.Confidence, Views: views,
	}, nil
}

// PolyConsensusTick 每小時彙整一次(自我閘門到每小時桶;首份只顯示不推播)。
func (s *Store) PolyConsensusTick() {
	if s.maiW == nil {
		return
	}
	now := time.Now()
	h := now.UTC().Unix() / 3600
	s.polyMu.RLock()
	skip := h == s.polyBucket || now.Before(s.polyRetryAt)
	s.polyMu.RUnlock()
	if skip {
		return
	}

	reps, err := polymarket.Screen(20)
	if err != nil {
		s.polyMu.Lock()
		s.polyRetryAt = now.Add(10 * time.Minute)
		s.polyMu.Unlock()
		log.Printf("poly-consensus: Screen 失敗: %v (10 分鐘後重試)", err)
		return
	}
	label := "跟單篩選AI彙整(" + s.maiW.Provider() + ")"
	raw, err := s.maiW.Analyze(polySystem, buildPolyUser(reps))
	if err != nil {
		s.polyMu.Lock()
		s.polyRetryAt = now.Add(5 * time.Minute)
		s.polyMu.Unlock()
		log.Printf("poly-consensus: AI(%s)失敗: %v (5 分鐘後重試)", s.maiW.Provider(), err)
		s.apiFail(label, err.Error())
		return
	}
	data, err := parsePolyAI(raw, reps)
	if err != nil {
		s.polyMu.Lock()
		s.polyRetryAt = now.Add(5 * time.Minute)
		s.polyMu.Unlock()
		log.Printf("poly-consensus: %v; raw=%.200s", err, raw)
		s.apiFail(label, err.Error())
		return
	}
	s.apiOK(label)
	data.UpdatedAt = now.Format("2006-01-02 15:04")
	data.Source = s.maiW.Provider()

	s.polyMu.Lock()
	s.polyData = data
	s.polyBucket = h
	seeded := s.polySeeded
	s.polySeeded = true
	s.polyMu.Unlock()
	log.Printf("poly-consensus: 已更新(整體=%s/%s,共 %d 人)via %s", data.Lean, data.Confidence, len(data.Views), s.maiW.Provider())

	if seeded && s.PolyPushEnabled() {
		body := data.Summary
		if r := []rune(body); len(r) > 90 {
			body = string(r[:90]) + "…"
		}
		s.PushSend("🧭 跟單篩選 · 聰明錢共識", body, "/polyscout")
	}
}

// PolyConsensusBoard 回目前快取的彙整(給 admin 端點)。
func (s *Store) PolyConsensusBoard() PolyConsensus {
	s.polyMu.RLock()
	d := s.polyData
	s.polyMu.RUnlock()
	d.PushOn = s.PolyPushEnabled()
	if d.Views == nil {
		d.Views = []PolyView{}
	}
	return d
}

// PolyPushEnabled 回傳聰明錢共識推播是否開啟(後台可設定,預設關閉)。
func (s *Store) PolyPushEnabled() bool {
	return s.db != nil && s.db.getConfig("polyscout_push") == "1"
}

// SetPolyPush 設定聰明錢共識推播開關(管理員)。
func (s *Store) SetPolyPush(on bool) {
	if s.db == nil {
		return
	}
	v := "0"
	if on {
		v = "1"
	}
	s.db.setConfig("polyscout_push", v)
}
