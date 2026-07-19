package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"datahunter/internal/auth"
	"datahunter/internal/cache"
)

type Server struct {
	store  *cache.Store
	secret string // token-signing secret

	loginRL    *rateLimiter // brute-force guard: per IP+username
	registerRL *rateLimiter // disk-fill guard: per IP
}

func NewServer(store *cache.Store, secret string) *Server {
	return &Server{
		store:      store,
		secret:     secret,
		loginRL:    newRateLimiter(5, time.Minute),    // 5 attempts / min / (ip+acct)
		registerRL: newRateLimiter(3, 10*time.Minute), // 3 registrations / 10 min / ip
	}
}

// roleOf extracts the caller's role from the Bearer token, then confirms it
// against the LIVE DB (token is only a hint): a banned/pending/deleted user, or
// one whose role changed, takes effect immediately rather than at token expiry.
func (s *Server) roleOf(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return auth.RolePublic
	}
	user, _, err := auth.ParseToken(strings.TrimPrefix(h, "Bearer "), s.secret)
	if err != nil {
		return auth.RolePublic
	}
	role, status, ok := s.store.LiveRoleStatus(user)
	if !ok || status != "active" {
		return auth.RolePublic // banned / pending / removed → no access
	}
	return role
}

// userOf returns the caller's username from a valid token ("" otherwise).
func (s *Server) userOf(r *http.Request) string {
	u, _, err := auth.ParseToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.secret)
	if err != nil {
		return ""
	}
	return u
}

// gate wraps a handler with a minimum-role requirement.
func (s *Server) gate(min string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.AtLeast(s.roleOf(r), min) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// gateTab is gate() for endpoints whose required role is admin-configurable
// (cache/tabperm.go). Hiding a tab in the UI is cosmetic — the caller can still
// hit the API — so the same table has to gate the data, resolved per request so
// an admin change takes effect immediately.
func (s *Server) gateTab(tab string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.AtLeast(s.roleOf(r), s.store.TabRole(tab)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// VIP 不再直接出現在這裡:所有 VIP 路由都改走 gateTab(角色由後台設定決定)
	P, M, A := auth.RolePublic, auth.RoleMember, auth.RoleAdmin

	// auth
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/register", s.handleRegister)
	mux.HandleFunc("/api/auth/me", s.handleMe)
	mux.HandleFunc("/api/admin/users", s.gate(A, s.handleAdminUsers))

	// uploaded images (asset proofs, article images, logo, QR), read-only.
	// noDirListing: Go's FileServer would otherwise render an index for
	// /uploads/proofs/ etc., letting anyone enumerate member asset proofs.
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", noDirListing(http.FileServer(http.Dir(uploadDir)))))

	// 內容分頁:預設公開,但角色一樣由「標籤權限」決定 —— 管理員把某個公開分頁
	// 調高成會員/VIP 時,API 必須跟著擋,否則只是在導覽列藏起來而已。
	mux.HandleFunc("/api/ranking", s.gateTab("ranking", s.handleRanking))
	mux.HandleFunc("/api/events", s.gateTab("events", s.handleEvents))
	mux.HandleFunc("/api/liquidations", s.gateTab("flow", s.handleLiquidations))
	mux.HandleFunc("/api/upbit", s.gateTab("upbit", s.handleUpbit))             // Upbit announcements (zh-TW)
	mux.HandleFunc("/api/news", s.gateTab("news", s.handleNews))                // GDELT market headlines (zh-TW)
	mux.HandleFunc("/api/funding", s.gateTab("funding", s.handleFunding))       // OKX funding-rate board
	mux.HandleFunc("/api/unlock", s.gateTab("unlock", s.handleUnlock))          // DefiLlama token-unlock board
	mux.HandleFunc("/api/robinhood", s.gateTab("robinhood", s.handleRobinhood)) // Robinhood 上架 board
	mux.HandleFunc("/api/sectors", s.gateTab("sectors", s.handleSectors))       // 板塊強弱/輪動(每整點)

	// 基礎設施 / 首頁共用資料,不屬於任何分頁,固定公開
	mux.HandleFunc("/api/home", s.gate(P, s.handleHome))
	mux.HandleFunc("/api/risk", s.gate(P, s.handleRisk))
	mux.HandleFunc("/api/market-ai", s.gate(P, s.handleMarketAI))            // 首頁整點大盤分析橫幅
	mux.HandleFunc("/api/strat-meta", s.gate(P, s.handleStratMeta))          // 各策略類型標籤 + 風控警語旗標
	mux.HandleFunc("/api/tab-perms", s.gate(P, s.handleTabPerms))            // 各分頁所需最低身分(給前端決定顯示哪些)
	mux.HandleFunc("/api/btc-sr", s.gate(P, s.handleBTCSR))                  // BTC 支撐壓力(戰場城牆用;全幣種 SR 仍為 VIP)
	mux.HandleFunc("/api/config", s.gate(P, s.handleConfig))                 // logo / social / QR
	mux.HandleFunc("/api/notice", s.gate(M, s.handleNotice))                 // login 公告彈窗 (members)
	mux.HandleFunc("/api/articles", s.gateTab("articles", s.handleArticles)) // column list
	mux.HandleFunc("/api/articles/", s.gate(P, s.handleArticleOne))

	// admin content management
	mux.HandleFunc("/api/admin/config", s.gate(A, s.handleAdminConfig))
	mux.HandleFunc("/api/admin/notice", s.gate(A, s.handleAdminNotice)) // 設定登入公告彈窗
	mux.HandleFunc("/api/admin/upload", s.gate(A, s.handleAdminUpload))
	mux.HandleFunc("/api/admin/articles", s.gate(A, s.handleAdminArticles))
	mux.HandleFunc("/api/admin/article-pin", s.gate(A, s.handleAdminArticlePin))  // 置頂/取消置頂
	mux.HandleFunc("/api/admin/export", s.gate(A, s.handleExport))                // strategy trades → CSV
	mux.HandleFunc("/api/admin/push-test", s.gate(A, s.handlePushTest))           // fire a test Web Push
	mux.HandleFunc("/api/admin/push-broadcast", s.gate(A, s.handlePushBroadcast)) // targeted group push
	mux.HandleFunc("/api/admin/push-reset", s.gate(A, s.handlePushReset))         // regen VAPID keys + clear subs
	mux.HandleFunc("/api/admin/ema-close", s.gate(A, s.handleEMAClose))           // 銀河: 手動出場 (admin-only)
	mux.HandleFunc("/api/admin/manual-exit", s.gate(A, s.handleManualExit))       // 各策略手動出場(動能衰弱)
	mux.HandleFunc("/api/admin/strat-states", s.gate(A, s.handleStratStates))     // 策略開關狀態
	mux.HandleFunc("/api/admin/strat-toggle", s.gate(A, s.handleStratToggle))     // 開/關某策略進場
	mux.HandleFunc("/api/admin/strat-config", s.gate(A, s.handleStratConfig))     // 策略設定(類型/風控/止損上限/保本/分批)
	mux.HandleFunc("/api/admin/tab-perms", s.gate(A, s.handleAdminTabPerms))      // 各身分組可見標籤(GET 列表 / POST 修改)
	// 策略頁:角色改由「標籤權限」決定(預設 冥王星=VIP、其餘觀察書=管理員),
	// 這樣後台可以把某一本策略開放給 VIP 而不必改程式。
	mux.HandleFunc("/api/conv", s.gateTab("conv", s.handleConv)) // 冥王星 (動態ATR均線收斂 4H)
	mux.HandleFunc("/api/admin/bollfade", s.gateTab("bollfade", s.handleBollFade))
	mux.HandleFunc("/api/admin/meanrev", s.gateTab("meanrev", s.handleMeanRev))
	mux.HandleFunc("/api/admin/bgv2", s.gateTab("bgv2", s.handleBGV2))
	mux.HandleFunc("/api/admin/bollema", s.gateTab("bollema", s.handleBollEMA))
	mux.HandleFunc("/api/admin/strat-clear", s.gate(A, s.handleStratClear)) // 清空某策略模擬單

	// 以下路由的角色由「標籤權限」設定決定(預設值同原本寫死的角色),
	// 管理員在後台調整後立即生效 — 前端隱藏分頁只是外觀,這裡才是真正的門。
	mux.HandleFunc("/api/oi-cache", s.gateTab("oi", s.handleOICache))
	mux.HandleFunc("/api/signals", s.gateTab("signals", s.handleSignals))
	mux.HandleFunc("/api/radar", s.gateTab("radar", s.handleRadar))
	mux.HandleFunc("/api/scorelog", s.gateTab("scorelog", s.handleScoreLog))
	mux.HandleFunc("/api/klines", s.gateTab("oi", s.handleKlines))
	// 注意:「幣種一覽」分頁本身是公開的,但個別幣種的詳細資料仍是會員限定 —
	// 兩者不是同一件事,別把這條接到 coins 分頁的權限上。
	mux.HandleFunc("/api/coin/", s.gate(M, s.handleCoinDetail))

	// VIP (live entries with TP/SL)
	mux.HandleFunc("/api/paper", s.gateTab("paper", s.handlePaper))
	mux.HandleFunc("/api/gamble", s.gateTab("gamble", s.handleGamble))
	mux.HandleFunc("/api/ema-only", s.gateTab("emaonly", s.handleEMAOnly))
	mux.HandleFunc("/api/sr", s.gateTab("sr", s.handleSR)) // 支撐壓力監控

	// 推薦系統
	mux.HandleFunc("/api/referral", s.gate(M, s.handleReferral))                      // 我的推廣(帳號遮罩)
	mux.HandleFunc("/api/referral/apply", s.gate(M, s.handleReferralApply))           // 申請下一檔獎勵
	mux.HandleFunc("/api/admin/referrals", s.gate(A, s.handleAdminReferrals))         // 推廣管理:名單 + 申請
	mux.HandleFunc("/api/admin/referral-ok", s.gate(A, s.handleAdminReferralOK))      // 切換「合格」
	mux.HandleFunc("/api/admin/referral-approve", s.gate(A, s.handleAdminRefApprove)) // 審核獎勵:通過
	mux.HandleFunc("/api/admin/referral-of", s.gate(A, s.handleAdminReferralOf))      // 某用戶的推廣名單(全名)
	mux.HandleFunc("/api/admin/merch-stock", s.gate(A, s.handleAdminMerchStock))      // 周邊庫存管理
	mux.HandleFunc("/api/referral/rules", s.gate(M, s.handleRefRules))               // 推廣規則(會員,未發佈回空)
	mux.HandleFunc("/api/admin/referral-rules", s.gate(A, s.handleAdminRefRules))    // 推廣規則:編輯/發佈

	// web push (PWA notifications)
	mux.HandleFunc("/api/push/key", s.gate(M, s.handlePushKey))
	mux.HandleFunc("/api/push/subscribe", s.gate(M, s.handlePushSubscribe))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return cors(mux)
}

// handleLogin: POST {username,password} → {token, role}.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Username, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !s.loginRL.allow(clientIP(r) + "|" + in.Username) {
		http.Error(w, "嘗試次數過多,請一分鐘後再試", http.StatusTooManyRequests)
		return
	}
	role, status, ok := s.store.Authenticate(in.Username, in.Password)
	if !ok {
		http.Error(w, "帳號或密碼錯誤", http.StatusUnauthorized)
		return
	}
	switch status {
	case "active":
		// ok
	case "pending":
		http.Error(w, "帳號審核中,請待管理員審核通過後再登入", http.StatusForbidden)
		return
	default: // banned / anything else
		http.Error(w, "帳號已停用,請聯繫管理員", http.StatusForbidden)
		return
	}
	// No idle logout: the session token is effectively permanent (10y). Bans /
	// deletions are still enforced immediately — every request is live-gated
	// against the DB (roleOf) and the SPA re-checks /api/auth/me every 15s.
	// Rotating JWT_SECRET is the "force-logout everyone" switch.
	token := auth.IssueToken(in.Username, role, s.secret, 3650*24*time.Hour)
	writeJSON(w, map[string]any{"token": token, "username": in.Username, "role": role})
}

// handleRegister: multipart self-registration → pending review.
// fields: username, password, uid, exchange (備註), proof (image file).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.registerRL.allow(clientIP(r)) {
		http.Error(w, "註冊過於頻繁,請稍後再試", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+512<<10) // image cap + form slack
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "圖片過大(上限 3MB)或表單格式錯誤", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	uid := strings.TrimSpace(r.FormValue("uid"))
	exchange := strings.TrimSpace(r.FormValue("exchange"))

	// validate the account BEFORE anything touches the disk, so an invalid /
	// duplicate registration can never leave an orphan proof file behind.
	if err := s.store.PrecheckRegister(username, password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	proofPath := ""
	if f, hdr, err := r.FormFile("proof"); err == nil {
		defer f.Close()
		if hdr.Size > maxUploadBytes {
			http.Error(w, errImageTooLarge.Error(), http.StatusBadRequest)
			return
		}
		p, err := saveUpload("proofs", username, hdr.Filename, f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		proofPath = p
	}
	// 推薦碼:註冊是「唯一」會綁定推薦人的時機。無效的碼在 store 層會被當成沒帶。
	refCode := strings.TrimSpace(r.FormValue("referralCode"))
	if err := s.store.Register(username, password, uid, exchange, proofPath, refCode); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.store.NotifyNewRegister(username, uid, exchange) // alert admins to review
	writeJSON(w, map[string]any{"ok": true, "status": "pending"})
}

// handleMe returns the caller's LIVE role + status (from the DB, not just the
// token) so the frontend can gate on every page load: an expired token → public
// (idle timeout), a banned/pending user → status back so the SPA kicks them out.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	user, _, err := auth.ParseToken(strings.TrimPrefix(h, "Bearer "), s.secret)
	if !strings.HasPrefix(h, "Bearer ") || err != nil {
		writeJSON(w, map[string]any{"role": auth.RolePublic})
		return
	}
	role, status, ok := s.store.LiveRoleStatus(user)
	if !ok {
		writeJSON(w, map[string]any{"role": auth.RolePublic})
		return
	}
	writeJSON(w, map[string]any{"username": user, "role": role, "status": status})
}

// handleAdminUsers: GET lists users; POST creates; PUT sets role/status. Admin only.
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.Users())
	case http.MethodPost:
		var in struct{ Username, Password, Role, Status string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Role == "" {
			in.Role = auth.RoleMember
		}
		if in.Status == "" {
			in.Status = "active"
		}
		if err := s.store.CreateUser(in.Username, in.Password, in.Role, in.Status); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodPut:
		var in struct{ Username, Role, Status string }
		json.NewDecoder(r.Body).Decode(&in)
		// whitelist the values (a typo like "vp" would silently brick the account)
		// and never let an admin demote/disable THEMSELVES (self-lockout).
		if in.Username == s.userOf(r) {
			http.Error(w, "無法修改自己的權限/狀態", http.StatusBadRequest)
			return
		}
		switch in.Role {
		case auth.RoleMember, auth.RoleVIP, auth.RoleAdmin:
		default:
			http.Error(w, "無效的角色", http.StatusBadRequest)
			return
		}
		switch in.Status {
		case "active", "pending", "banned":
		default:
			http.Error(w, "無效的狀態", http.StatusBadRequest)
			return
		}
		s.store.SetUserRole(in.Username, in.Role, in.Status)
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		u := r.URL.Query().Get("username")
		if u == "" || u == s.userOf(r) { // never delete yourself
			http.Error(w, "無法刪除此帳號", http.StatusBadRequest)
			return
		}
		s.store.DeleteUser(u)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleRanking serves the public long/short Top-10 (scores only, no levels).
func (s *Server) handleRanking(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Ranking())
}

func (s *Server) handleOICache(w http.ResponseWriter, r *http.Request) {
	data, updated := s.store.All()
	writeJSON(w, map[string]any{
		"updated_at": updated.Format(time.RFC3339),
		"data":       data,
	})
}

// signal row for the feed-style view
type signalRow struct {
	Coin    string  `json:"coin"`
	Score   int     `json:"score"`
	Bias    string  `json:"bias"`
	Quality string  `json:"quality"`
	OIChg1h float64 `json:"oi_chg_1h"`
	OKXChg  float64 `json:"okx_chg"`
	CVD     float64 `json:"cvd_ratio"`
	Funding float64 `json:"funding_rate"`
}

func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	data, _ := s.store.All()
	rows := make([]signalRow, 0, len(data))
	for coin, snap := range data {
		// only surface coins with a meaningful score, like the anomaly feed
		if snap.Score > -20 && snap.Score < 20 {
			continue
		}
		rows = append(rows, signalRow{
			Coin: coin, Score: snap.Score, Bias: snap.Bias, Quality: snap.Quality,
			OIChg1h: snap.OIChg1h, OKXChg: snap.OKXChg, CVD: snap.CVDRatio, Funding: snap.Funding,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return abs(rows[i].Score) > abs(rows[j].Score)
	})
	writeJSON(w, rows)
}

// handleHome serves the landing-page payload (market, recs, altcoin season).
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	home, err := s.store.Home()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, home)
}

// handleRadar serves the breakout radar (potential pumps/dumps across market).
func (s *Server) handleRadar(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Radar())
}

// handlePaper serves the disciplined paper-trading tracker.
func (s *Server) handlePaper(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Paper())
}

// handleGamble serves the loose "gamble" paper-trading tracker.
func (s *Server) handleGamble(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Gamble())
}

// handleExport streams a strategy book's full trade history as CSV (admin only).
// ?book=main|gamble|emaonly. A UTF-8 BOM is emitted so Excel opens
// the Chinese/number columns correctly.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	book := r.URL.Query().Get("book")
	switch book {
	case "main", "gamble", "emaonly":
	default:
		http.Error(w, "unknown book", http.StatusBadRequest)
		return
	}
	trades := s.store.ExportTrades(book)
	fname := fmt.Sprintf("%s-%s.csv", book, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+fname)
	w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
	cw := csv.NewWriter(w)
	cw.Write([]string{"coin", "dir", "score", "entry", "tp", "sl", "exit", "pnl_pct",
		"status", "outcome", "oi", "cvd", "funding", "open_time", "close_time"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, t := range trades {
		closeT := ""
		if t.CloseTime != nil {
			closeT = t.CloseTime.UTC().Format(time.RFC3339)
		}
		cw.Write([]string{
			t.Coin, t.Dir, strconv.Itoa(t.Score), f(t.Entry), f(t.TP), f(t.SL), f(t.Cur),
			f(t.PnLPct), t.Status, t.Outcome, f(t.OI), f(t.CVD), f(t.Funding),
			t.OpenTime.UTC().Format(time.RFC3339), closeT,
		})
	}
	cw.Flush()
}

// handleEMAOnly serves the standalone "EMA策略" tracker (1h cross + 15m EMA200).
func (s *Server) handleEMAOnly(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.EMAOnly())
}

// handleConv serves the 冥王星 (動態ATR均線收斂 4H) strategy tracker. VIP.
func (s *Server) handleConv(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.ConvState())
}


// handleBollFade serves the admin-only 布林重回 1h strategy tracker.
func (s *Server) handleBollFade(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.BollFadeState())
}

// handleMeanRev serves the admin-only 乖離回歸 1h strategy tracker.
func (s *Server) handleMeanRev(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.MeanRevState())
}

// handleBGV2 serves the admin-only 布乖v2 tracker — both legs merged into one payload.
func (s *Server) handleBGV2(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.BGV2State())
}

// ---- 推薦系統 ----

// handleReferral serves 我的推廣 for the caller. Referred account names are masked
// server-side — the full names never leave the box.
func (s *Server) handleReferral(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Referral(s.userOf(r)))
}

// handleReferralApply books the caller's next reward tier. 每 10 個合格解鎖一檔,
// 累計不消耗;DB 的 UNIQUE(username,tier) 擋掉重複申請。
func (s *Server) handleReferralApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	user := s.userOf(r)
	if user == "" {
		http.Error(w, "未登入", http.StatusUnauthorized)
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		kind = "usdt" // 舊前端沒帶 kind → 維持原本的 30U 行為
	}
	if err := s.store.ApplyReward(user, kind); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminMerchStock 設定/查詢周邊庫存總量。GET 回傳現況,POST {total} 設定。
func (s *Server) handleAdminMerchStock(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("total")))
		if err != nil || n < 0 {
			http.Error(w, "庫存總量必須是 0 或正整數", http.StatusBadRequest)
			return
		}
		if !s.store.SetMerchStock(n) {
			http.Error(w, "設定失敗", http.StatusInternalServerError)
			return
		}
	}
	total, used, left := s.store.MerchStock()
	writeJSON(w, map[string]any{"total": total, "used": used, "left": left})
}

// handleAdminReferrals serves 推廣管理: every member's counts + all applications.
func (s *Server) handleAdminReferrals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.ReferralAdmin())
}

// handleAdminReferralOK flips a referred account's 合格 flag. POST {username, ok}.
func (s *Server) handleAdminReferralOK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Username string `json:"username"`
		OK       bool   `json:"ok"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Username == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !s.store.SetRefOK(in.Username, in.OK) {
		http.Error(w, "查無此用戶", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminRefApprove marks a reward application 通過. POST {id}.
// 發放本身是人工的 — this only records the sign-off.
func (s *Server) handleAdminRefApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		ID int64 `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID == 0 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !s.store.ApproveReward(in.ID) {
		http.Error(w, "查無此申請或已審核", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminReferralOf serves one user's referral list in FULL (admin drill-down
// from 使用者管理 — names unmasked). GET ?user=xxx
func (s *Server) handleAdminReferralOf(w http.ResponseWriter, r *http.Request) {
	u := strings.TrimSpace(r.URL.Query().Get("user"))
	if u == "" {
		http.Error(w, "missing user", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.store.ReferralOf(u))
}

// handleBollEMA serves the admin-only 布林EMA (4H 突破蓄勢) tracker.
func (s *Server) handleBollEMA(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.BollEMAState())
}

// handleStratClear (admin): POST ?book=<name>[&scope=closed] resets a strategy's
// simulated trades (memory + DB). scope=closed keeps open positions and drops only
// the closed history; otherwise everything is wiped.
func (s *Server) handleStratClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	closedOnly := r.URL.Query().Get("scope") == "closed"
	if !s.store.ClearStrategy(r.URL.Query().Get("book"), closedOnly) {
		http.Error(w, "unknown book", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleEMAClose force-closes an open 銀河 (EMA-only) trade at market, recorded as
// 逾時 (expired). Admin only. POST {id}.
func (s *Server) handleEMAClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ ID string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !s.store.ManualCloseEMA(in.ID) {
		http.Error(w, "找不到此進行中的部位(可能已平倉)", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleManualExit (admin): POST {book, id} force-closes an open trade at market,
// recorded as 動能衰弱. Covers every strategy except 銀河 (which uses ema-close).
func (s *Server) handleManualExit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Book, ID string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Book == "" || in.ID == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if !s.store.ManualExit(in.Book, in.ID) {
		http.Error(w, "找不到此進行中的部位(可能已平倉)", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleStratStates (admin): the on/off state of every strategy.
func (s *Server) handleStratStates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.StrategyStates())
}

// handleStratToggle (admin): POST {name, on} enables/disables a strategy's entries.
func (s *Server) handleStratToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Name string
		On   bool
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	s.store.SetStrategyEnabled(in.Name, in.On)
	writeJSON(w, map[string]any{"ok": true})
}

// handleStratConfig saves one strategy's admin tuning (類型 / 風控警語 / 最大止損% /
// 保本 / 分批止盈). Takes effect on the NEXT entry — open trades keep their levels.
func (s *Server) handleStratConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Name  string         `json:"name"`
		Cfg   cache.StratCfg `json:"cfg"`
		Reset bool           `json:"reset"` // true: 丟掉覆寫,回到程式預設
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if in.Reset {
		if !s.store.ResetStrategyConfig(in.Name) {
			http.Error(w, "unknown strategy", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if !s.store.SetStrategyConfig(in.Name, in.Cfg) {
		http.Error(w, "unknown strategy", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleStratMeta serves the PUBLIC per-strategy meta (類型 tags + 風控警語 flag)
// that the strategy pages render. Admin-only fields are not exposed here.
func (s *Server) handleStratMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.StrategyMeta())
}

// handleTabPerms serves the PUBLIC tab→minimum-role map so the nav can hide what
// the caller can't reach. Exposing the requirement is harmless; the data itself
// is still gated server-side by gateTab.
func (s *Server) handleTabPerms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.VisibleTabs())
}

// handleAdminTabPerms lists (GET) or updates (POST) the tab permission table.
func (s *Server) handleAdminTabPerms(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			Tab  string `json:"tab"`
			Role string `json:"role"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Tab == "" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if !s.store.SetTabRole(in.Tab, in.Role) {
			http.Error(w, "unknown tab, bad role, or locked", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	writeJSON(w, s.store.TabPerms())
}

// handleScoreLog serves the log of when coins crossed the ±20 signal line.
func (s *Server) handleScoreLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.ScoreLog())
}

// handleRisk serves the US/macro risk-backdrop strip.
func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Risk())
}

// handleLiquidations serves the recent liquidation feed.
func (s *Server) handleLiquidations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Liquidations())
}

// handleBTCSR serves BTC's support/resistance only — the public 戰場 draws its
// fortress walls from it. The full multi-coin monitor stays VIP (handleSR).
func (s *Server) handleBTCSR(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.store.BTCSR()) }

// handleSR serves the VIP 支撐壓力 monitor: mainstream coins' current support &
// resistance levels and breach status (no trades).
func (s *Server) handleSR(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.SR())
}

// handleUpbit serves the recent Upbit announcements (Korean titles translated to
// Traditional Chinese), newest first.
func (s *Server) handleUpbit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.UpbitBoard())
}

// handleNews serves the GDELT market-moving headlines (translated to Traditional
// Chinese), newest first.
func (s *Server) handleNews(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.News())
}

// handleFunding serves the OKX funding-rate board.
func (s *Server) handleFunding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.FundingBoard())
}

// handleUnlock serves the DefiLlama token-unlock board.
func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Unlocks())
}

// handleRobinhood serves the Robinhood 上架 board.
func (s *Server) handleRobinhood(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.RobinhoodBoard())
}

// handleMarketAI serves the hourly 大盤 AI 分析.
func (s *Server) handleMarketAI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.MarketAI())
}

// handleSectors serves the hourly 板塊強弱/輪動 board.
func (s *Server) handleSectors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.SectorBoard())
}

// handleEvents serves the full high-impact US economic calendar.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Events())
}

// handleKlines serves recent OHLC candles for the detail-drawer chart.
func (s *Server) handleKlines(w http.ResponseWriter, r *http.Request) {
	coin := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("coin")))
	if coin == "" {
		http.Error(w, "coin required", http.StatusBadRequest)
		return
	}
	interval := r.URL.Query().Get("interval")
	switch interval {
	case "1h", "4h", "1d":
	default:
		interval = "1h"
	}
	kl, err := s.store.Klines(coin, interval, 260) // extra bars so EMA200 is valid
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, kl)
}

// handleCoinDetail serves the full per-coin score card at /api/coin/{COIN}.
// Data is fetched fresh on each request for the requested coin.
func (s *Server) handleCoinDetail(w http.ResponseWriter, r *http.Request) {
	coin := strings.ToUpper(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/coin/"), "/"))
	if coin == "" {
		http.Error(w, "coin required", http.StatusBadRequest)
		return
	}
	detail, err := s.store.Detail(coin)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, detail)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// noDirListing 404s directory requests so FileServer can't render an index of
// /uploads/* (member asset proofs must not be enumerable).
func noDirListing(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
