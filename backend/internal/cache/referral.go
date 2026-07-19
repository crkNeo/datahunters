package cache

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// referral.go: 推薦系統.
//
//	・每個用戶有一個固定的推薦碼 (users.ref_code, DB-unique, lazily backfilled).
//	・分享網址 /?referralCode=XXXX → 訪客註冊時綁定 (users.ref_by), 綁定後永不變更.
//	  綁定「只」發生在註冊,所以自我推薦在結構上不可能(新用戶的碼是註冊當下才生的).
//	・「合格」(users.ref_ok) 完全由管理員人工切換,可雙向.
//	・每 10 個合格解鎖「一次兌換額度」:可用額度 = ⌊合格/10⌋ − 已申請次數.
//	  已核發不回溯(合格被改回未達成只擋未來的申請) — 實際發放為人工.
//	・額度可以換兩種獎勵,各消耗一次:
//	    usdt  — 30 USDT,只要有額度就能換.
//	    merch — BITUNIX 限量周邊,另需 合格 ≥ 20、終生限一組、且庫存尚有.
//	  所以剛好 20 分的人是「1×30U + 1 組周邊」,不是 2×30U.
//	・每人每月(當地時區的自然月)最多兌換 refMonthlyCap 次.
const (
	refPerTier     = 10 // 每 N 個合格解鎖一次兌換額度
	refUSDT        = 30 // usdt 檔的金額(USDT),純顯示用,實際發放為人工
	refMerchAt     = 20 // 周邊的合格人數門檻
	refMonthlyCap  = 10 // 每人每月兌換次數上限
	merchStockKey  = "merch_stock" // site_config:周邊總庫存(後台可調)
	merchStockInit = 0             // 沒設定過就是 0 → 按鈕關閉,不會超發
)

// 獎勵品項。DB 存的是這兩個字串,別直接寫字面值。
const (
	kindUSDT  = "usdt"
	kindMerch = "merch"
)

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
	Tier      int    `json:"tier"`      // 第幾次兌換
	Qualified int    `json:"qualified"` // 申請當下的合格人數
	Kind      string `json:"kind"`      // usdt | merch
	Status    string `json:"status"`    // pending | approved
	Applied   int64  `json:"applied"`
	Reviewed  int64  `json:"reviewed"`
}

func (db *DB) refRewards(username string) []RefReward { // "" = all users (admin)
	q := `SELECT id,username,tier,qualified,COALESCE(kind,'usdt'),status,COALESCE(applied,0),COALESCE(reviewed,0) FROM referral_rewards`
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
		if rows.Scan(&r.ID, &r.Username, &r.Tier, &r.Qualified, &r.Kind, &r.Status, &r.Applied, &r.Reviewed) != nil {
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

// monthStartMs 是「當月 1 號 00:00」的毫秒時戳,用伺服器本地時區 —— 活動是給
// 台灣用戶看的,用 UTC 會讓月初/月底那幾個小時的額度對不上用戶看到的日期。
func monthStartMs(now time.Time) int64 {
	y, m, _ := now.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, now.Location()).UnixMilli()
}

// refMonthCount 是這個自然月已經兌換的次數(月上限用)。
func (db *DB) refMonthCount(username string, now time.Time) int {
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM referral_rewards
	  WHERE username=? COLLATE utf8mb4_bin AND applied >= ?`,
		username, monthStartMs(now)).Scan(&n)
	return n
}

// merchClaimed 是全站已申請的周邊數(不分審核狀態 —— 申請就佔庫存,否則會超發)。
func (db *DB) merchClaimed() int {
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM referral_rewards WHERE kind=?`, kindMerch).Scan(&n)
	return n
}

// merchTaken 回報這個人是否已經領過周邊(終生限一組;DB 的 uk_merch_once 是真正的
// 保證,這裡只是給 UI 先把按鈕關掉)。
func (db *DB) merchTaken(username string) bool {
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM referral_rewards
	  WHERE username=? COLLATE utf8mb4_bin AND kind=?`, username, kindMerch).Scan(&n)
	return n > 0
}

// MerchStock 回傳(總量, 已申請, 剩餘)。總量存在 site_config,沒設定過就是 0。
func (s *Store) MerchStock() (total, used, left int) {
	if s.db == nil {
		return 0, 0, 0
	}
	total = merchStockInit
	if v := s.db.getConfig(merchStockKey); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			total = n
		}
	}
	used = s.db.merchClaimed()
	if left = total - used; left < 0 {
		left = 0 // 後台把總量調到低於已發放數 → 顯示 0,不要出現負數
	}
	return
}

// SetMerchStock 設定周邊總量(後台)。不能低於 0;允許低於已申請數(等同停止發放)。
func (s *Store) SetMerchStock(n int) bool {
	if s.db == nil || n < 0 {
		return false
	}
	s.SetConfig(merchStockKey, strconv.Itoa(n))
	return true
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

	// 兩種獎勵各自的可用狀態。CanApply 只說「還有額度」,這兩個才是按鈕的開關。
	CanUSDT  bool `json:"can_usdt"`
	CanMerch bool `json:"can_merch"`
	// 按鈕關閉的原因,直接顯示給用戶看(空字串 = 可申請)。
	USDTWhy  string `json:"usdt_why"`
	MerchWhy string `json:"merch_why"`

	USDTAmt    int  `json:"usdt_amt"`    // 30
	MerchAt    int  `json:"merch_at"`    // 周邊門檻:20
	MerchLeft  int  `json:"merch_left"`  // 剩餘庫存
	MerchTotal int  `json:"merch_total"` // 庫存總量
	MerchTaken bool `json:"merch_taken"` // 我已經領過周邊了
	MonthUsed  int  `json:"month_used"`  // 本月已兌換次數
	MonthCap   int  `json:"month_cap"`   // 10
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

	now := time.Now()
	st.USDTAmt, st.MerchAt, st.MonthCap = refUSDT, refMerchAt, refMonthlyCap
	st.MonthUsed = s.db.refMonthCount(username, now)
	st.MerchTotal, _, st.MerchLeft = s.MerchStock()
	st.MerchTaken = s.db.merchTaken(username)
	// 按鈕開關和真正申請時的檢查走同一支函式,不會出現「按鈕亮著但按下去被打槍」。
	st.USDTWhy = s.rewardBlocker(username, kindUSDT, st.Qualified, st.Applied, now)
	st.MerchWhy = s.rewardBlocker(username, kindMerch, st.Qualified, st.Applied, now)
	st.CanUSDT, st.CanMerch = st.USDTWhy == "", st.MerchWhy == ""
	return st
}

// rewardBlocker 回傳「不能申請的理由」,可以申請就回空字串。這是兌換規則的唯一
// 真相來源 —— RefState 用它決定按鈕,ApplyReward 用它擋請求。
//
// 額度是兩種獎勵共用的:每 10 個合格解鎖一次,換 30U 或換周邊各消耗一次。
// 所以剛好 20 分的人有 2 次額度,可以「1×30U + 1 組周邊」。
func (s *Store) rewardBlocker(username, kind string, qualified, applied int, now time.Time) string {
	if s.db == nil {
		return "尚未啟用"
	}
	if used := s.db.refMonthCount(username, now); used >= refMonthlyCap {
		return fmt.Sprintf("本月兌換次數已達上限(%d 次)", refMonthlyCap)
	}
	if qualified/refPerTier <= applied {
		need := (applied+1)*refPerTier - qualified
		return fmt.Sprintf("額度不足,還差 %d 位合格受邀戶", need)
	}
	if kind == kindMerch {
		if qualified < refMerchAt {
			return fmt.Sprintf("周邊需要累積滿 %d 積分(目前 %d)", refMerchAt, qualified)
		}
		if s.db.merchTaken(username) {
			return "周邊每人限兌換一組"
		}
		if _, _, left := s.MerchStock(); left <= 0 {
			return "周邊已兌換完畢"
		}
	}
	return ""
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
// ApplyReward books one 兌換 for username. kind 是 usdt 或 merch。
//
// 併發安全靠 DB 的兩個 UNIQUE:uk_user_tier 擋掉「同一次額度被連點兩下用掉兩次」,
// uk_merch_once 擋掉「同一人領到兩組周邊」。rewardBlocker 的檢查會有 TOCTOU 空窗,
// 但真正會超發的兩件事都被索引擋死了,剩下的(庫存)由審核階段人工把關。
func (s *Store) ApplyReward(username, kind string) error {
	if s.db == nil {
		return fmt.Errorf("尚未啟用")
	}
	if kind != kindUSDT && kind != kindMerch {
		return fmt.Errorf("獎勵品項不正確")
	}
	now := time.Now()
	_, qualified := s.db.refCounts(username)
	applied := s.db.refRewardCount(username)
	if why := s.rewardBlocker(username, kind, qualified, applied, now); why != "" {
		return fmt.Errorf("%s", why)
	}
	tier := applied + 1
	// merch_key = 帳號 → uk_merch_once 讓每人只塞得下一列;usdt 填 NULL 不受限。
	var merchKey any
	if kind == kindMerch {
		merchKey = username
	}
	_, err := s.db.sql.Exec(`INSERT INTO referral_rewards(username,tier,qualified,kind,merch_key,status,applied)
	  VALUES(?,?,?,?,?,'pending',?)`, username, tier, qualified, kind, merchKey, now.UnixMilli())
	if err != nil {
		if kind == kindMerch {
			return fmt.Errorf("周邊每人限兌換一組")
		}
		return fmt.Errorf("這次額度已申請過了,請重新整理")
	}
	s.NotifyRewardApply(username, tier, qualified, kind)
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

// RefAdminRow is one member on the 推廣管理 board. Total/Qualified/Applied describe
// them AS A REFERRER; RefBy/OK describe them AS A REFERRED account — the 合格 toggle
// lives on the latter, which is why RefBy has to travel with it (an admin flipping
// 合格 must see whose count it credits).
type RefAdminRow struct {
	Username  string `json:"username"`
	Code      string `json:"code"`
	Role      string `json:"role"`
	Total     int    `json:"total"`
	Qualified int    `json:"qualified"`
	Applied   int    `json:"applied"`
	RefBy     string `json:"ref_by"` // 推薦人("" = 自然註冊,合格對他無意義)
	OK        bool   `json:"ok"`     // 此帳號本身是否已被判定合格
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
	  SELECT u.username, COALESCE(u.ref_code,''), u.role, u.ref_by, u.ref_ok,
	         (SELECT COUNT(*) FROM users r WHERE r.ref_by = u.username) AS total,
	         (SELECT COALESCE(SUM(r.ref_ok),0) FROM users r WHERE r.ref_by = u.username) AS qualified,
	         (SELECT COUNT(*) FROM referral_rewards w WHERE w.username = u.username) AS applied
	  FROM users u
	  ORDER BY (u.ref_by <> '' AND u.ref_ok = 0) DESC, total DESC, u.created ASC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r RefAdminRow
		var ok int
		if rows.Scan(&r.Username, &r.Code, &r.Role, &r.RefBy, &ok, &r.Total, &r.Qualified, &r.Applied) != nil {
			continue
		}
		r.OK = ok == 1
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
func (s *Store) NotifyRewardApply(username string, tier, qualified int, kind string) {
	item := fmt.Sprintf("%d USDT", refUSDT)
	if kind == kindMerch {
		_, _, left := s.MerchStock()
		item = fmt.Sprintf("BITUNIX 周邊(剩餘庫存 %d)", left)
	}
	if s.notifier != nil && s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("🎁 <b>推薦獎勵申請</b>\n帳號 %s\n品項 %s\n第 %d 次兌換\n合格人數 %d\n請至後台審核",
			username, item, tier, qualified))
	}
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			go s.pushMgr.SendTo(subs, "🎁 推薦獎勵申請",
				fmt.Sprintf("%s 申請 %s(合格 %d 人)", username, item, qualified), "/?tab=referral")
		}
	}
}
