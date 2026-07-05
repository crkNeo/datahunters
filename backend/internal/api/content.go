package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"datahunter/internal/cache"
)

// handlePushKey (member): returns the VAPID public key for the browser to
// subscribe with. Empty when web-push is unavailable.
func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"key": s.store.PushKey()})
}

// handlePushSubscribe (member): body = the PushSubscription JSON from the browser.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if len(body) == 0 {
		http.Error(w, "empty", http.StatusBadRequest)
		return
	}
	s.store.Subscribe(s.userOf(r), string(body))
	writeJSON(w, map[string]any{"ok": true})
}

// handlePushTest (admin): fires an immediate Web Push to all subscribers so the
// pipeline can be verified without waiting for a trade-open event. Returns the
// current subscriber count so an empty subscription list is obvious.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	subs := len(s.store.AllSubs())
	s.store.PushSend("JMCH 測試推播", "看到這則代表推播管線正常 ✅", "/")
	writeJSON(w, map[string]any{"ok": true, "subs": subs})
}

// handlePushReset (admin): regenerate VAPID keys and clear subscriptions, then
// return the new public key so the caller can re-subscribe immediately.
func (s *Server) handlePushReset(w http.ResponseWriter, r *http.Request) {
	s.store.ResetPush()
	writeJSON(w, map[string]any{"ok": true, "key": s.store.PushKey()})
}

// handleConfig (public): returns site settings (logo, social links JSON, QR).
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.SiteConfig())
}

// handleAdminConfig (admin): POST {key, value} upserts one setting.
func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Key, Value string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Key == "" {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	s.store.SetConfig(in.Key, in.Value)
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminUpload (admin): multipart "file" + "sub" → { path }. General image
// upload for logo / QR / article cover / article body images.
func (s *Server) handleAdminUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+512<<10)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "圖片過大(上限 3MB)或表單格式錯誤", http.StatusBadRequest)
		return
	}
	sub := r.FormValue("sub")
	switch sub {
	case "logo", "qr", "articles", "social":
	default:
		sub = "misc"
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "缺少檔案", http.StatusBadRequest)
		return
	}
	defer f.Close()
	if hdr.Size > maxUploadBytes {
		http.Error(w, errImageTooLarge.Error(), http.StatusBadRequest)
		return
	}
	path, err := saveUpload(sub, "img", hdr.Filename, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": path})
}

// handleArticles (public): GET list (without full bodies is fine, but we return
// full so the SPA can render detail without a second call).
func (s *Server) handleArticles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Articles())
}

// handleArticleOne (public): GET /api/articles/{id}.
func (s *Server) handleArticleOne(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/articles/"), 10, 64)
	a, ok := s.store.Article(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, a)
}

// handleAdminArticles (admin): POST creates/updates (id==0 → create), DELETE ?id= removes.
func (s *Server) handleAdminArticles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var a cache.Article
		if json.NewDecoder(r.Body).Decode(&a) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if a.Tags == nil {
			a.Tags = []string{}
		}
		if a.Blocks == nil {
			a.Blocks = []cache.ArticleBlock{}
		}
		id := s.store.SaveArticle(&a)
		writeJSON(w, map[string]any{"ok": true, "id": id})
	case http.MethodDelete:
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		s.store.DeleteArticle(id)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
