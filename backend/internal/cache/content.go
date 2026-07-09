package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// subID hashes a push endpoint into a fixed 64-char key (endpoints are too long
// to index directly on MySQL).
func subID(endpoint string) string {
	h := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(h[:])
}

// ---- site config: logo, social links, QR (admin-editable key/value) ----

func (db *DB) setConfig(k, v string) {
	db.sql.Exec(`INSERT INTO site_config(k,v) VALUES(?,?)
	  ON DUPLICATE KEY UPDATE v=VALUES(v)`, k, v)
}

func (db *DB) allConfig() map[string]string {
	out := map[string]string{}
	rows, err := db.sql.Query(`SELECT k,v FROM site_config`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out[k] = v
		}
	}
	return out
}

// publicConfigKeys is the whitelist of site_config entries that /api/config may
// expose. Anything not listed (e.g. vapid_priv, the Web Push PRIVATE key) stays
// server-side — a whitelist means future secrets can't leak by being added.
var publicConfigKeys = map[string]bool{
	"logo": true, "social": true, "qr": true, "qr_link": true,
}

// SiteConfig returns the PUBLIC site settings only (logo, social JSON, qr, qr_link).
func (s *Store) SiteConfig() map[string]string {
	if s.db == nil {
		return map[string]string{}
	}
	all := s.db.allConfig()
	out := make(map[string]string, len(publicConfigKeys))
	for k := range publicConfigKeys {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}

// SetConfig upserts one config key (admin only).
func (s *Store) SetConfig(k, v string) {
	if s.db != nil {
		s.db.setConfig(k, v)
	}
}

func (db *DB) getConfig(k string) string {
	var v string
	db.sql.QueryRow(`SELECT v FROM site_config WHERE k=?`, k).Scan(&v)
	return v
}

// GetConfig returns one config value ("" if unset). Used by the push manager
// for the VAPID keypair.
func (s *Store) GetConfig(k string) string {
	if s.db == nil {
		return ""
	}
	return s.db.getConfig(k)
}

// ---- web-push subscriptions (push.Backend) ----

func (db *DB) addSub(endpoint, username, sub string) {
	db.sql.Exec(`INSERT INTO push_subs(id,endpoint,username,sub) VALUES(?,?,?,?)
	  ON DUPLICATE KEY UPDATE endpoint=VALUES(endpoint), username=VALUES(username), sub=VALUES(sub)`,
		subID(endpoint), endpoint, username, sub)
}

func (db *DB) allSubs() []string {
	rows, err := db.sql.Query(`SELECT sub FROM push_subs`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if rows.Scan(&sub) == nil {
			out = append(out, sub)
		}
	}
	return out
}

func (db *DB) delSub(endpoint string) {
	db.sql.Exec(`DELETE FROM push_subs WHERE id=?`, subID(endpoint))
}

func (db *DB) clearSubs() { db.sql.Exec(`DELETE FROM push_subs`) }

// adminSubs returns the push subscription rows belonging to admin accounts, for
// targeted admin-only alerts (e.g. a new registration to review).
func (db *DB) adminSubs() []string {
	rows, err := db.sql.Query(`SELECT p.sub FROM push_subs p
	  JOIN users u ON u.username = p.username WHERE u.role = 'admin'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if rows.Scan(&sub) == nil {
			out = append(out, sub)
		}
	}
	return out
}

// subsByRole returns push subscription rows for users of exactly the given role.
func (db *DB) subsByRole(role string) []string {
	rows, err := db.sql.Query(`SELECT p.sub FROM push_subs p
	  JOIN users u ON u.username = p.username WHERE u.role = ?`, role)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if rows.Scan(&sub) == nil {
			out = append(out, sub)
		}
	}
	return out
}

// PushBroadcast sends an immediate Web Push to a user group and returns how many
// subscriptions were targeted. group is one of all|member|vip|admin; url is the
// deep-link opened on tap (e.g. "/" or "/?tab=articles&article=12").
func (s *Store) PushBroadcast(title, body, group, url string) int {
	var subs []string
	switch group {
	case "all":
		subs = s.AllSubs()
	case "member", "vip", "admin":
		if s.db != nil {
			subs = s.db.subsByRole(group)
		}
	default:
		return 0
	}
	if s.pushMgr != nil && len(subs) > 0 {
		s.pushMgr.SendTo(subs, title, body, url)
	}
	return len(subs)
}

// NotifyNewRegister alerts admins that a new account is awaiting review — via
// Telegram (admin chat) and Web Push (admin subscribers only, not all members).
func (s *Store) NotifyNewRegister(username, uid, notes string) {
	if s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("🆕 <b>新用戶註冊</b>\n帳號 %s\nUID %s\n交易所 %s\n請至後台審核",
			username, orDash(uid), orDash(notes)))
	}
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			s.pushMgr.SendTo(subs, "🆕 新用戶註冊", username+" 待審核", "/")
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ResetPush regenerates the VAPID keypair and drops all stored subscriptions
// (they were made with the old key). Clients re-subscribe on next load.
func (s *Store) ResetPush() {
	if s.pushMgr != nil {
		s.pushMgr.Reset()
	}
	if s.db != nil {
		s.db.clearSubs()
	}
}

// AllSubs / DelSub / AddSub satisfy push.Backend + the subscribe handler.
func (s *Store) AllSubs() []string {
	if s.db == nil {
		return nil
	}
	return s.db.allSubs()
}

func (s *Store) DelSub(endpoint string) {
	if s.db != nil {
		s.db.delSub(endpoint)
	}
}

// Subscribe stores a browser push subscription (parses its endpoint as the key).
func (s *Store) Subscribe(username, subJSON string) {
	if s.db == nil {
		return
	}
	var e struct {
		Endpoint string `json:"endpoint"`
	}
	if json.Unmarshal([]byte(subJSON), &e) != nil || e.Endpoint == "" {
		return
	}
	s.db.addSub(e.Endpoint, username, subJSON)
}

// PushKey returns the VAPID public key ("" if push is unavailable).
func (s *Store) PushKey() string {
	if s.pushMgr == nil {
		return ""
	}
	return s.pushMgr.PublicKey()
}

// PushSend delivers a Web Push notification to all subscribers (best-effort).
func (s *Store) PushSend(title, body, url string) {
	if s.pushMgr != nil {
		s.pushMgr.Send(title, body, url)
	}
}

// ---- articles (block-based: paragraphs + images) ----

// ArticleBlock is one piece of article body: a text paragraph or an image.
type ArticleBlock struct {
	Type  string `json:"type"` // "text" | "image"
	Text  string `json:"text,omitempty"`
	Image string `json:"image,omitempty"` // /uploads/... path
}

// Article is one column post.
type Article struct {
	ID      int64          `json:"id"`
	Title   string         `json:"title"`
	Cover   string         `json:"cover"` // /uploads/... path
	Tags    []string       `json:"tags"`
	Blocks  []ArticleBlock `json:"blocks"`
	Created int64          `json:"created"`
	Updated int64          `json:"updated"`
	Pinned  bool           `json:"pinned"` // 置頂:pinned posts sort to the top
}

func splitTags(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func scanArticle(scan func(...any) error) (Article, error) {
	var a Article
	var tags, blocks string
	var pinned int
	if err := scan(&a.ID, &a.Title, &a.Cover, &tags, &blocks, &a.Created, &a.Updated, &pinned); err != nil {
		return a, err
	}
	a.Pinned = pinned != 0
	a.Tags = splitTags(tags)
	if json.Unmarshal([]byte(blocks), &a.Blocks) != nil || a.Blocks == nil {
		a.Blocks = []ArticleBlock{}
	}
	return a, nil
}

const articleCols = `id,title,cover,tags,blocks,created,updated,pinned`

func (db *DB) articleList() []Article {
	rows, err := db.sql.Query(`SELECT ` + articleCols + ` FROM articles ORDER BY pinned DESC, created DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Article{}
	for rows.Next() {
		if a, err := scanArticle(rows.Scan); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func (db *DB) articleGet(id int64) (Article, bool) {
	row := db.sql.QueryRow(`SELECT `+articleCols+` FROM articles WHERE id=?`, id)
	a, err := scanArticle(row.Scan)
	return a, err == nil
}

func (db *DB) articleUpsert(a *Article) int64 {
	tags := strings.Join(a.Tags, ",")
	blocks, _ := json.Marshal(a.Blocks)
	now := time.Now().UnixMilli()
	if a.ID == 0 {
		res, err := db.sql.Exec(`INSERT INTO articles(title,cover,tags,blocks,created,updated) VALUES(?,?,?,?,?,?)`,
			a.Title, a.Cover, tags, string(blocks), now, now)
		if err != nil {
			return 0
		}
		id, _ := res.LastInsertId()
		return id
	}
	db.sql.Exec(`UPDATE articles SET title=?,cover=?,tags=?,blocks=?,updated=? WHERE id=?`,
		a.Title, a.Cover, tags, string(blocks), now, a.ID)
	return a.ID
}

func (db *DB) articleDelete(id int64) { db.sql.Exec(`DELETE FROM articles WHERE id=?`, id) }

func (db *DB) articleSetPinned(id int64, pinned bool) {
	p := 0
	if pinned {
		p = 1
	}
	db.sql.Exec(`UPDATE articles SET pinned=? WHERE id=?`, p, id)
}

// Store wrappers.
func (s *Store) Articles() []Article {
	if s.db == nil {
		return []Article{}
	}
	return s.db.articleList()
}

func (s *Store) Article(id int64) (Article, bool) {
	if s.db == nil {
		return Article{}, false
	}
	return s.db.articleGet(id)
}

func (s *Store) SaveArticle(a *Article) int64 {
	if s.db == nil {
		return 0
	}
	return s.db.articleUpsert(a)
}

func (s *Store) DeleteArticle(id int64) {
	if s.db != nil {
		s.db.articleDelete(id)
	}
}

// SetArticlePinned pins/unpins a post (admin). Pinned posts sort to the top.
func (s *Store) SetArticlePinned(id int64, pinned bool) {
	if s.db != nil {
		s.db.articleSetPinned(id, pinned)
	}
}
