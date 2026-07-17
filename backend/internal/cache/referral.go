package cache

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// referral.go: 推薦系統.
//
//	・每個用戶有一個固定的推薦碼 (users.ref_code, DB-unique, lazily backfilled).
//	・分享網址 /?referralCode=XXXX → 訪客註冊時綁定 (users.ref_by), 綁定後永不變更.
//	  綁定「只」發生在註冊,所以自我推薦在結構上不可能(新用戶的碼是註冊當下才生的).
//	・「合格」(users.ref_ok) 完全由管理員人工切換,可雙向.
//	・每 10 個合格解鎖一檔獎勵,累計不消耗:可申請 = ⌊合格/10⌋ − 已申請次數.
//	  已核發不回溯(合格被改回未達成只擋未來的申請) — 實際發放為人工.
const refPerTier = 10 // 每 N 個合格解鎖一檔

// refAlphabet omits 0/O/1/I/L — codes get read aloud and retyped.
const refAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

const refCodeLen = 8

// newRefCode returns a random code. Uniqueness is enforced by the DB's uk_ref_code;
// callers retry on a duplicate-key error.
func newRefCode() string {
	b := make([]byte, refCodeLen)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is fatal-ish; fall back to time-based so registration
		// never hard-fails on it (the DB unique index still guards correctness).
		return fmt.Sprintf("T%07d", time.Now().UnixNano()%1e7)
	}
	out := make([]byte, refCodeLen)
	for i, v := range b {
		out[i] = refAlphabet[int(v)%len(refAlphabet)]
	}
	return string(out)
}

// ---- DB layer ----

// refCodeOf returns the user's code, generating and storing one on first use.
func (db *DB) refCodeOf(username string) string {
	var code sql.NullString
	if err := db.sql.QueryRow(`SELECT ref_code FROM users WHERE username=? COLLATE utf8mb4_bin`, username).Scan(&code); err != nil {
		return ""
	}
	if code.Valid && code.String != "" {
		return code.String
	}
	for i := 0; i < 8; i++ { // retry on the (rare) unique collision
		c := newRefCode()
		res, err := db.sql.Exec(`UPDATE users SET ref_code=? WHERE username=? COLLATE utf8mb4_bin AND ref_code IS NULL`, c, username)
		if err != nil {
			continue // duplicate code → try another
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return c
		}
		// someone else set it first (or the user vanished) → re-read
		if db.sql.QueryRow(`SELECT ref_code FROM users WHERE username=? COLLATE utf8mb4_bin`, username).Scan(&code) == nil && code.Valid {
			return code.String
		}
		return ""
	}
	return ""
}

// backfillRefCodes mints a code for every account that lacks one. Without this the
// 推廣管理 board shows "—" for anyone who never opened 我的推廣 (codes were minted
// lazily on first view), and an admin can't hand out a code on a user's behalf.
// Idempotent; runs once at startup.
func (db *DB) backfillRefCodes() {
	rows, err := db.sql.Query(`SELECT username FROM users WHERE ref_code IS NULL OR ref_code=''`)
	if err != nil {
		return
	}
	var names []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			names = append(names, u)
		}
	}
	rows.Close() // close before the UPDATEs below — MySQL can't reuse the conn mid-scan
	for _, u := range names {
		db.refCodeOf(u)
	}
	if len(names) > 0 {
		log.Printf("referral: backfilled %d referral code(s)", len(names))
	}
}

// userByRefCode resolves a referral code to its owner ("" if unknown).
func (db *DB) userByRefCode(code string) string {
	if code == "" {
		return ""
	}
	var u string
	if db.sql.QueryRow(`SELECT username FROM users WHERE ref_code=?`, strings.ToUpper(strings.TrimSpace(code))).Scan(&u) != nil {
		return ""
	}
	return u
}

// refCounts returns (總推薦人數, 合格人數) for a referrer.
// 總推薦人數 counts every bound account including pending ones (用戶決定:算).
func (db *DB) refCounts(username string) (total, qualified int) {
	db.sql.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ref_ok),0) FROM users WHERE ref_by=? COLLATE utf8mb4_bin`, username).Scan(&total, &qualified)
	return
}

// RefRecord is one referred account as shown on 我的推廣 (name masked) or in the
// admin drill-down (name in full).
type RefRecord struct {
	Username string `json:"username"`
	Status   string `json:"status"` // active | pending | banned
	OK       bool   `json:"ok"`     // 合格(管理員審核)
	Created  int64  `json:"created"`
}

// refList returns the accounts a user referred, newest first.
func (db *DB) refList(username string) []RefRecord {
	rows, err := db.sql.Query(`SELECT username,status,ref_ok,created FROM users
	  WHERE ref_by=? COLLATE utf8mb4_bin ORDER BY created DESC`, username)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []RefRecord{}
	for rows.Next() {
		var r RefRecord
		var ok int
		if rows.Scan(&r.Username, &r.Status, &ok, &r.Created) != nil {
			continue
		}
		r.OK = ok == 1
		out = append(out, r)
	}
	return out
}

// RefReward is one 獎勵申請.
type RefReward struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Tier      int    `json:"tier"`      // 1 = 10 人, 2 = 20 人, …
	Qualified int    `json:"qualified"` // 申請當下的合格人數
	Status    string `json:"status"`    // pending | approved
	Applied   int64  `json:"applied"`
	Reviewed  int64  `json:"reviewed"`
}

func (db *DB) refRewards(username string) []RefReward { // "" = all users (admin)
	q := `SELECT id,username,tier,qualified,status,COALESCE(applied,0),COALESCE(reviewed,0) FROM referral_rewards`
	args := []any{}
	if username != "" {
		q += ` WHERE username=? COLLATE utf8mb4_bin`
		args = append(args, username)
	}
	q += ` ORDER BY applied DESC`
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []RefReward{}
	for rows.Next() {
		var r RefReward
		if rows.Scan(&r.ID, &r.Username, &r.Tier, &r.Qualified, &r.Status, &r.Applied, &r.Reviewed) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (db *DB) refRewardCount(username string) int {
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM referral_rewards WHERE username=? COLLATE utf8mb4_bin`, username).Scan(&n)
	return n
}

// ---- Store API ----

// RefState is the 我的推廣 payload (member).
type RefState struct {
	Code      string      `json:"code"`
	Total     int         `json:"total"`     // 總推薦人數(含待審核)
	Qualified int         `json:"qualified"` // 合格人數
	Applied   int         `json:"applied"`   // 已申請次數
	CanApply  bool        `json:"can_apply"` // ⌊合格/10⌋ > 已申請
	NextTier  int         `json:"next_tier"` // 下一檔的檔次(1,2,3…)
	NextNeed  int         `json:"next_need"` // 還差幾個合格才能解鎖下一檔
	PerTier   int         `json:"per_tier"`  // 10
	Records   []RefRecord `json:"records"`   // 推薦紀錄(帳號已遮罩)
	Rewards   []RefReward `json:"rewards"`   // 我的申請紀錄
}

// maskName hides most of an account name: 推薦紀錄 shows who signed up, but the
// referrer has no business seeing full account names.
func maskName(s string) string {
	r := []rune(s)
	switch {
	case len(r) <= 2:
		return string(r[:1]) + "***"
	case len(r) <= 4:
		return string(r[:2]) + "***"
	}
	return string(r[:3]) + "***"
}

// Referral returns the caller's 我的推廣 state (codes are minted on first view).
func (s *Store) Referral(username string) RefState {
	st := RefState{PerTier: refPerTier, Records: []RefRecord{}, Rewards: []RefReward{}}
	if s.db == nil || username == "" {
		return st
	}
	st.Code = s.db.refCodeOf(username)
	st.Total, st.Qualified = s.db.refCounts(username)
	st.Applied = s.db.refRewardCount(username)
	for _, r := range s.db.refList(username) {
		r.Username = maskName(r.Username) // 遮罩:全名不離開伺服器
		st.Records = append(st.Records, r)
	}
	st.Rewards = s.db.refRewards(username)
	st.CanApply, st.NextTier, st.NextNeed = refTier(st.Qualified, st.Applied)
	return st
}

// refTier is the reward rule, isolated so it can be tested without a DB:
// 每 refPerTier 個合格解鎖一檔,累計不消耗 → 可申請 = ⌊合格/10⌋ − 已申請.
// 合格被改回未達成時 canApply 自然變 false,但已申請的不回溯(發放為人工).
func refTier(qualified, applied int) (canApply bool, nextTier, nextNeed int) {
	canApply = qualified/refPerTier > applied
	nextTier = applied + 1
	if need := nextTier*refPerTier - qualified; need > 0 {
		nextNeed = need
	}
	return
}

// ApplyReward books the next reward tier for username. The DB's UNIQUE(username,
// tier) is the real guard — a double-click can't book the same tier twice.
func (s *Store) ApplyReward(username string) error {
	if s.db == nil {
		return fmt.Errorf("尚未啟用")
	}
	_, qualified := s.db.refCounts(username)
	applied := s.db.refRewardCount(username)
	tier := applied + 1
	if qualified/refPerTier < tier {
		return fmt.Errorf("合格人數不足:第 %d 檔需要 %d 位合格,目前 %d 位", tier, tier*refPerTier, qualified)
	}
	_, err := s.db.sql.Exec(`INSERT INTO referral_rewards(username,tier,qualified,status,applied)
	  VALUES(?,?,?,'pending',?)`, username, tier, qualified, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("這一檔已申請過了")
	}
	s.NotifyRewardApply(username, tier, qualified)
	return nil
}

// SetRefOK flips a referred account's 合格 flag (admin, 可雙向).
func (s *Store) SetRefOK(username string, ok bool) bool {
	if s.db == nil {
		return false
	}
	v := 0
	if ok {
		v = 1
	}
	res, err := s.db.sql.Exec(`UPDATE users SET ref_ok=? WHERE username=? COLLATE utf8mb4_bin`, v, username)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ApproveReward marks an application 通過 (admin). 發放本身是人工的 — this only
// records that the admin signed off.
func (s *Store) ApproveReward(id int64) bool {
	if s.db == nil {
		return false
	}
	res, err := s.db.sql.Exec(`UPDATE referral_rewards SET status='approved', reviewed=? WHERE id=? AND status='pending'`,
		time.Now().UnixMilli(), id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// RefAdminRow is one member on the 推廣管理 board.
type RefAdminRow struct {
	Username  string `json:"username"`
	Code      string `json:"code"`
	Role      string `json:"role"`
	Total     int    `json:"total"`
	Qualified int    `json:"qualified"`
	Applied   int    `json:"applied"`
}

// RefAdmin is the 推廣管理 payload: every member's counts + all reward applications.
type RefAdmin struct {
	Rows    []RefAdminRow `json:"rows"`
	Rewards []RefReward   `json:"rewards"`
	Pending int           `json:"pending"` // 待審核的申請數(給徽章用)
}

// ReferralAdmin builds the 推廣管理 board in ONE pass — a per-user query loop would
// be N+1 round-trips as the member list grows.
func (s *Store) ReferralAdmin() RefAdmin {
	out := RefAdmin{Rows: []RefAdminRow{}, Rewards: []RefReward{}}
	if s.db == nil {
		return out
	}
	rows, err := s.db.sql.Query(`
	  SELECT u.username, COALESCE(u.ref_code,''), u.role,
	         (SELECT COUNT(*) FROM users r WHERE r.ref_by = u.username) AS total,
	         (SELECT COALESCE(SUM(r.ref_ok),0) FROM users r WHERE r.ref_by = u.username) AS qualified,
	         (SELECT COUNT(*) FROM referral_rewards w WHERE w.username = u.username) AS applied
	  FROM users u ORDER BY total DESC, u.created ASC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r RefAdminRow
		if rows.Scan(&r.Username, &r.Code, &r.Role, &r.Total, &r.Qualified, &r.Applied) != nil {
			continue
		}
		out.Rows = append(out.Rows, r)
	}
	out.Rewards = s.db.refRewards("")
	for _, w := range out.Rewards {
		if w.Status == "pending" {
			out.Pending++
		}
	}
	return out
}

// ReferralOf returns one user's referral list in FULL (admin drill-down from
// 使用者管理). Unlike Referral() the names are not masked — admins need them.
func (s *Store) ReferralOf(username string) RefState {
	st := RefState{PerTier: refPerTier, Records: []RefRecord{}, Rewards: []RefReward{}}
	if s.db == nil || username == "" {
		return st
	}
	st.Code = s.db.refCodeOf(username)
	st.Total, st.Qualified = s.db.refCounts(username)
	st.Applied = s.db.refRewardCount(username)
	st.Records = s.db.refList(username)
	st.Rewards = s.db.refRewards(username)
	return st
}

// NotifyRewardApply alerts admins that a reward application needs review — same
// channels as NotifyNewRegister (Telegram + admin Web Push).
func (s *Store) NotifyRewardApply(username string, tier, qualified int) {
	if s.notifier != nil && s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("🎁 <b>推薦獎勵申請</b>\n帳號 %s\n第 %d 檔(%d 人)\n合格人數 %d\n請至後台審核",
			username, tier, tier*refPerTier, qualified))
	}
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			go s.pushMgr.SendTo(subs, "🎁 推薦獎勵申請",
				fmt.Sprintf("%s 申請第 %d 檔(合格 %d 人)", username, tier, qualified), "/?tab=referral")
		}
	}
}
