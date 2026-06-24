package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"datahunter/internal/cache"
)

type Server struct {
	store *cache.Store
}

func NewServer(store *cache.Store) *Server {
	return &Server{store: store}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oi-cache", s.handleOICache)
	mux.HandleFunc("/api/signals", s.handleSignals)
	mux.HandleFunc("/api/home", s.handleHome)
	mux.HandleFunc("/api/radar", s.handleRadar)
	mux.HandleFunc("/api/paper", s.handlePaper)
	mux.HandleFunc("/api/gamble", s.handleGamble)
	mux.HandleFunc("/api/scorelog", s.handleScoreLog)
	mux.HandleFunc("/api/options", s.handleOptions)
	mux.HandleFunc("/api/coin/", s.handleCoinDetail)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return cors(mux)
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

// handleScoreLog serves the log of when coins crossed the ±20 signal line.
func (s *Server) handleScoreLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.ScoreLog())
}

// handleOptions serves the BTC/ETH options-derived dashboard.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Options())
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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
