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

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	P, M, V, A := auth.RolePublic, auth.RoleMember, auth.RoleVIP, auth.RoleAdmin

	// auth
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/register", s.handleRegister)
	mux.HandleFunc("/api/auth/me", s.handleMe)
	mux.HandleFunc("/api/admin/users", s.gate(A, s.handleAdminUsers))

	// uploaded images (asset proofs, article images, logo, QR), read-only.
	// noDirListing: Go's FileServer would otherwise render an index for
	// /uploads/proofs/ etc., letting anyone enumerate member asset proofs.
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", noDirListing(http.FileServer(http.Dir(uploadDir)))))

	// public (no login)
	mux.HandleFunc("/api/ranking", s.gate(P, s.handleRanking))
	mux.HandleFunc("/api/home", s.gate(P, s.handleHome))
	mux.HandleFunc("/api/events", s.gate(P, s.handleEvents))
	mux.HandleFunc("/api/risk", s.gate(P, s.handleRisk))
	mux.HandleFunc("/api/liquidations", s.gate(P, s.handleLiquidations))
	mux.HandleFunc("/api/upbit", s.gate(P, s.handleUpbit))         // Upbit announcements (zh-TW)
	mux.HandleFunc("/api/news", s.gate(P, s.handleNews))           // GDELT market headlines (zh-TW)
	mux.HandleFunc("/api/funding", s.gate(P, s.handleFunding))     // OKX funding-rate board
	mux.HandleFunc("/api/unlock", s.gate(P, s.handleUnlock))       // DefiLlama token-unlock board
	mux.HandleFunc("/api/robinhood", s.gate(P, s.handleRobinhood)) // Robinhood 上架 board
	mux.HandleFunc("/api/market-ai", s.gate(P, s.handleMarketAI))  // 大盤 AI 分析(每整點)
	mux.HandleFunc("/api/sectors", s.gate(P, s.handleSectors))     // 板塊強弱/輪動(每整點)
	mux.HandleFunc("/api/btc-sr", s.gate(P, s.handleBTCSR))        // BTC 支撐壓力(戰場城牆用;全幣種 SR 仍為 VIP)
	mux.HandleFunc("/api/config", s.gate(P, s.handleConfig))       // logo / social / QR
	mux.HandleFunc("/api/notice", s.gate(M, s.handleNotice))       // login 公告彈窗 (members)
	mux.HandleFunc("/api/articles", s.gate(P, s.handleArticles))   // column list
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
	mux.HandleFunc("/api/conv", s.gate(V, s.handleConv))                          // 冥王星 (動態ATR均線收斂 4H, VIP)
	mux.HandleFunc("/api/admin/rsifade", s.gate(A, s.handleRSIFade))              // 逆勢超買空 30m (admin-only)
	mux.HandleFunc("/api/admin/bollfade", s.gate(A, s.handleBollFade))            // 布林重回 1h (admin-only)
	mux.HandleFunc("/api/admin/meanrev", s.gate(A, s.handleMeanRev))              // 乖離回歸 1h (admin-only)
	mux.HandleFunc("/api/admin/bgv2", s.gate(A, s.handleBGV2))                    // 布乖v2 兩腿家族 只做空 (admin-only)
	mux.HandleFunc("/api/admin/strat-clear", s.gate(A, s.handleStratClear))       // 清空某策略模擬單

	// members (logged in)
	mux.HandleFunc("/api/oi-cache", s.gate(M, s.handleOICache))
	mux.HandleFunc("/api/signals", s.gate(M, s.handleSignals))
	mux.HandleFunc("/api/radar", s.gate(M, s.handleRadar))
	mux.HandleFunc("/api/scorelog", s.gate(M, s.handleScoreLog))
	mux.HandleFunc("/api/klines", s.gate(M, s.handleKlines))
	mux.HandleFunc("/api/coin/", s.gate(M, s.handleCoinDetail))

	// VIP (live entries with TP/SL)
	mux.HandleFunc("/api/paper", s.gate(V, s.handlePaper))
	mux.HandleFunc("/api/gamble", s.gate(V, s.handleGamble))
	mux.HandleFunc("/api/ema-only", s.gate(V, s.handleEMAOnly))
	mux.HandleFunc("/api/sr", s.gate(V, s.handleSR)) // 支撐壓力監控 (VIP)

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
	if err := s.store.Register(username, password, uid, exchange, proofPath); err != nil {
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

// handleRSIFade serves the admin-only 逆勢超買空 30m strategy tracker.
func (s *Server) handleRSIFade(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.RSIFadeState())
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
