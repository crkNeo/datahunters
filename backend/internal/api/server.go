package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"datahunter/internal/auth"
	"datahunter/internal/cache"
)

type Server struct {
	store  *cache.Store
	secret string // token-signing secret
}

func NewServer(store *cache.Store, secret string) *Server {
	return &Server{store: store, secret: secret}
}

// roleOf extracts the caller's role from the Bearer token (default public).
func (s *Server) roleOf(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return auth.RolePublic
	}
	_, role, err := auth.ParseToken(strings.TrimPrefix(h, "Bearer "), s.secret)
	if err != nil {
		return auth.RolePublic
	}
	return role
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
	mux.HandleFunc("/api/auth/me", s.handleMe)
	mux.HandleFunc("/api/admin/users", s.gate(A, s.handleAdminUsers))

	// public (no login)
	mux.HandleFunc("/api/ranking", s.gate(P, s.handleRanking))
	mux.HandleFunc("/api/home", s.gate(P, s.handleHome))
	mux.HandleFunc("/api/events", s.gate(P, s.handleEvents))
	mux.HandleFunc("/api/risk", s.gate(P, s.handleRisk))
	mux.HandleFunc("/api/orderbook", s.gate(P, s.handleOrderBook))
	mux.HandleFunc("/api/liquidations", s.gate(P, s.handleLiquidations))

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
	mux.HandleFunc("/api/premium", s.gate(V, s.handlePremium))

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
	role, ok := s.store.Authenticate(in.Username, in.Password)
	if !ok {
		http.Error(w, "帳號或密碼錯誤", http.StatusUnauthorized)
		return
	}
	token := auth.IssueToken(in.Username, role, s.secret, 7*24*time.Hour)
	writeJSON(w, map[string]any{"token": token, "username": in.Username, "role": role})
}

// handleMe: returns the caller's username/role from their token.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	user, role, err := auth.ParseToken(strings.TrimPrefix(h, "Bearer "), s.secret)
	if !strings.HasPrefix(h, "Bearer ") || err != nil {
		writeJSON(w, map[string]any{"role": auth.RolePublic})
		return
	}
	writeJSON(w, map[string]any{"username": user, "role": role})
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
		s.store.SetUserRole(in.Username, in.Role, in.Status)
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

// handlePremium serves the aligned + funding-fuel control-group tracker.
func (s *Server) handlePremium(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Premium())
}

// handleScoreLog serves the log of when coins crossed the ±20 signal line.
func (s *Server) handleScoreLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.ScoreLog())
}

// handleRisk serves the US/macro risk-backdrop strip.
func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Risk())
}

// handleOrderBook serves the order-book wall/imbalance board.
func (s *Server) handleOrderBook(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.OrderBook())
}

// handleLiquidations serves the recent liquidation feed.
func (s *Server) handleLiquidations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Liquidations())
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
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
