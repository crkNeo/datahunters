package cache

import (
	"database/sql"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// db.go persists the two things we want to verify across restarts: the score
// cross log (±20 signals) and the paper-trade books, plus users / site config /
// articles / push subs for the member platform. Backed by MySQL (same server).

// Schema is the MySQL DDL. Each statement is executed separately (the driver
// does not run multi-statement Exec unless multiStatements=true). VARCHAR(191)
// is used for indexed/PK string columns so they fit the utf8mb4 index limit.
const Schema = `
CREATE TABLE IF NOT EXISTS score_events (
  ts    BIGINT NOT NULL,
  coin  VARCHAR(32) NOT NULL,
  score INT,
  bias  VARCHAR(16),
  price DOUBLE,
  KEY idx_se_ts (ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS paper_trades (
  id         VARCHAR(191) PRIMARY KEY,
  book       VARCHAR(32) NOT NULL,
  coin       VARCHAR(32), dir VARCHAR(8), score INT,
  entry      DOUBLE, tp DOUBLE, sl DOUBLE, cur DOUBLE, pnl_pct DOUBLE,
  status     VARCHAR(16), outcome VARCHAR(16),
  open_time  BIGINT, close_time BIGINT,
  oi DOUBLE DEFAULT 0, cvd DOUBLE DEFAULT 0, funding DOUBLE DEFAULT 0,
  tp1 DOUBLE NOT NULL DEFAULT 0, tp2 DOUBLE NOT NULL DEFAULT 0,
  legs TINYINT NOT NULL DEFAULT 0, filled DOUBLE NOT NULL DEFAULT 0, realized DOUBLE NOT NULL DEFAULT 0,
  be_hit TINYINT NOT NULL DEFAULT 0, be_price DOUBLE NOT NULL DEFAULT 0,
  KEY idx_pt_open (open_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS liquidations (
  ts BIGINT NOT NULL, coin VARCHAR(32) NOT NULL, side VARCHAR(8), px DOUBLE, usd DOUBLE,
  KEY idx_liq_ts (ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS users (
  username  VARCHAR(191) PRIMARY KEY,
  pass_hash VARCHAR(255) NOT NULL,
  role      VARCHAR(16) NOT NULL DEFAULT 'member',
  status    VARCHAR(16) NOT NULL DEFAULT 'active',
  uid       VARCHAR(64)  NOT NULL DEFAULT '',
  created   BIGINT,
  expiry    BIGINT DEFAULT 0,
  notes     VARCHAR(255) NOT NULL DEFAULT '',
  proof     VARCHAR(512) NOT NULL DEFAULT '',
  -- 推薦系統. ref_code is NULLable on purpose: MySQL's UNIQUE index permits many
  -- NULLs but only ONE '' — a NOT NULL DEFAULT '' column could never take a second
  -- user before backfill. ref_by is set ONCE at registration and never changes.
  ref_code  VARCHAR(16) NULL,
  ref_by    VARCHAR(191) NOT NULL DEFAULT '',
  ref_ok    TINYINT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_ref_code (ref_code),
  KEY idx_ref_by (ref_by)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 推薦獎勵申請. UNIQUE(username,tier) makes "每檔只能領一次" a DB-level guarantee,
-- so a double-click or two concurrent requests can never book the same tier twice.
CREATE TABLE IF NOT EXISTS referral_rewards (
  id        BIGINT AUTO_INCREMENT PRIMARY KEY,
  username  VARCHAR(191) NOT NULL,
  tier      INT NOT NULL,           -- 1 = 10 人, 2 = 20 人, …(每 10 個合格一檔)
  qualified INT NOT NULL,           -- 申請當下的合格人數(留證)
  status    VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending | approved
  applied   BIGINT,
  reviewed  BIGINT,
  UNIQUE KEY uk_user_tier (username, tier),
  KEY idx_rr_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS site_config (
  k VARCHAR(191) PRIMARY KEY,
  v LONGTEXT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS articles (
  id      BIGINT AUTO_INCREMENT PRIMARY KEY,
  title   VARCHAR(512) NOT NULL DEFAULT '',
  cover   VARCHAR(512) NOT NULL DEFAULT '',
  tags    VARCHAR(512) NOT NULL DEFAULT '',
  blocks  LONGTEXT,
  created BIGINT,
  updated BIGINT,
  pinned  TINYINT NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS push_subs (
  id       VARCHAR(64) PRIMARY KEY,   -- sha256(endpoint) hex, endpoints exceed the 191 index limit
  endpoint TEXT NOT NULL,
  username VARCHAR(191),
  sub      LONGTEXT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

// DB wraps the SQL handle for persistence.
type DB struct{ sql *sql.DB }

// OpenMySQL opens a MySQL connection, verifies it, and ensures the schema. It
// returns the raw *sql.DB so the migration tool can reuse it. dsn is the
// go-sql-driver DSN, e.g. "user:pass@tcp(127.0.0.1:3306)/datahunter?charset=utf8mb4".
func OpenMySQL(dsn string) (*sql.DB, error) {
	d, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	d.SetConnMaxLifetime(3 * time.Minute)
	d.SetMaxOpenConns(10)
	d.SetMaxIdleConns(5)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	// legacy push_subs used the (often >191-char) endpoint as PRIMARY KEY, which
	// made every subscription INSERT fail on MySQL. Rebuild it with a hashed id
	// PK. Safe to drop: subscriptions are transient and clients auto re-register.
	var tbl, hasID int
	d.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='push_subs'`).Scan(&tbl)
	d.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='push_subs' AND COLUMN_NAME='id'`).Scan(&hasID)
	if tbl > 0 && hasID == 0 {
		d.Exec("DROP TABLE push_subs")
	}
	// strip "-- ..." line comments before splitting on ';' so a ';' inside a
	// comment can never cut a statement in half.
	lines := strings.Split(Schema, "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "--"); idx >= 0 {
			lines[i] = ln[:idx]
		}
	}
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := d.Exec(stmt); err != nil {
			d.Close()
			return nil, err
		}
	}
	// add the articles.pinned column to pre-existing tables (CREATE ... IF NOT
	// EXISTS won't alter an already-created table). Idempotent: skip if present.
	var hasPinned int
	d.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='articles' AND COLUMN_NAME='pinned'`).Scan(&hasPinned)
	if hasPinned == 0 {
		d.Exec(`ALTER TABLE articles ADD COLUMN pinned TINYINT NOT NULL DEFAULT 0`)
	}
	// multi-TP (分批止盈) state columns on pre-existing paper_trades tables. Idempotent.
	for col, ddl := range map[string]string{
		"tp1":      "ADD COLUMN tp1 DOUBLE NOT NULL DEFAULT 0",
		"tp2":      "ADD COLUMN tp2 DOUBLE NOT NULL DEFAULT 0",
		"legs":     "ADD COLUMN legs TINYINT NOT NULL DEFAULT 0",
		"filled":   "ADD COLUMN filled DOUBLE NOT NULL DEFAULT 0",
		"realized": "ADD COLUMN realized DOUBLE NOT NULL DEFAULT 0",
		"be_hit":   "ADD COLUMN be_hit TINYINT NOT NULL DEFAULT 0",
		"be_price": "ADD COLUMN be_price DOUBLE NOT NULL DEFAULT 0",
	} {
		var has int
		d.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='paper_trades' AND COLUMN_NAME=?`, col).Scan(&has)
		if has == 0 {
			d.Exec(`ALTER TABLE paper_trades ` + ddl)
		}
	}
	// 推薦系統 columns on pre-existing users tables. Idempotent.
	for col, ddl := range map[string]string{
		"ref_code": "ADD COLUMN ref_code VARCHAR(16) NULL",
		"ref_by":   "ADD COLUMN ref_by VARCHAR(191) NOT NULL DEFAULT ''",
		"ref_ok":   "ADD COLUMN ref_ok TINYINT NOT NULL DEFAULT 0",
	} {
		var has int
		d.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME=?`, col).Scan(&has)
		if has == 0 {
			d.Exec(`ALTER TABLE users ` + ddl)
		}
	}
	// …and their indexes (ADD COLUMN doesn't bring the keys from the CREATE above).
	for idx, ddl := range map[string]string{
		"uk_ref_code": "ADD UNIQUE KEY uk_ref_code (ref_code)",
		"idx_ref_by":  "ADD KEY idx_ref_by (ref_by)",
	} {
		var has int
		d.QueryRow(`SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND INDEX_NAME=?`, idx).Scan(&has)
		if has == 0 {
			d.Exec(`ALTER TABLE users ` + ddl)
		}
	}
	return d, nil
}

func openDB(dsn string) (*DB, error) {
	d, err := OpenMySQL(dsn)
	if err != nil {
		return nil, err
	}
	return &DB{d}, nil
}

// User is an account row for the public web build.
type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	UID      string `json:"uid"`
	Created  int64  `json:"created"`
	Expiry   int64  `json:"expiry"`
	Notes    string `json:"notes"`
	Proof    string `json:"proof"` // asset-proof image URL path
}

func (db *DB) upsertUser(username, passHash, role, status string) {
	db.sql.Exec(`INSERT INTO users(username,pass_hash,role,status,created) VALUES(?,?,?,?,?)
	  ON DUPLICATE KEY UPDATE role=VALUES(role), status=VALUES(status)`,
		username, passHash, role, status, time.Now().UnixMilli())
}

// userAuth returns (passHash, role, status) for a username, ok=false if absent.
// COLLATE utf8mb4_bin forces a CASE-SENSITIVE match: "Hsuan" must not log into
// "hsuan" (MySQL's default ci collation would otherwise treat them as equal).
func (db *DB) userAuth(username string) (hash, role, status string, ok bool) {
	row := db.sql.QueryRow(`SELECT pass_hash,role,status FROM users WHERE username=? COLLATE utf8mb4_bin`, username)
	if err := row.Scan(&hash, &role, &status); err != nil {
		return "", "", "", false
	}
	return hash, role, status, true
}

// userRoleStatus returns the CURRENT role+status (no password), for live gating
// so bans / role changes take effect immediately, not at token expiry.
func (db *DB) userRoleStatus(username string) (role, status string, ok bool) {
	row := db.sql.QueryRow(`SELECT role,status FROM users WHERE username=? COLLATE utf8mb4_bin`, username)
	if err := row.Scan(&role, &status); err != nil {
		return "", "", false
	}
	return role, status, true
}

// registerUser inserts a self-registered account in "pending" review status.
func (db *DB) registerUser(username, passHash, uid, notes, proof, refBy string) {
	db.sql.Exec(`INSERT INTO users(username,pass_hash,role,status,uid,created,notes,proof,ref_by)
	  VALUES(?,?,?,?,?,?,?,?,?)`,
		username, passHash, "member", "pending", uid, time.Now().UnixMilli(), notes, proof, refBy)
	db.refCodeOf(username) // 立刻發自己的推薦碼(refCodeOf 生成並寫入)
}

func (db *DB) listUsers() []User {
	rows, err := db.sql.Query(`SELECT username,role,status,uid,created,expiry,notes,proof FROM users ORDER BY created DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if rows.Scan(&u.Username, &u.Role, &u.Status, &u.UID, &u.Created, &u.Expiry, &u.Notes, &u.Proof) == nil {
			out = append(out, u)
		}
	}
	return out
}

func (db *DB) setUserRole(username, role, status string) {
	db.sql.Exec(`UPDATE users SET role=?, status=? WHERE username=?`, role, status, username)
}

func (db *DB) deleteUser(username string) {
	db.sql.Exec(`DELETE FROM users WHERE username=?`, username)
	db.sql.Exec(`DELETE FROM push_subs WHERE username=?`, username) // clean their push subs too
}

func (db *DB) userExists(username string) bool {
	var n int
	db.sql.QueryRow(`SELECT count(*) FROM users WHERE username=?`, username).Scan(&n)
	return n > 0
}

func (db *DB) insertScoreEvent(e ScoreEvent) {
	db.sql.Exec(`INSERT INTO score_events(ts,coin,score,bias,price) VALUES(?,?,?,?,?)`,
		e.Time.UnixMilli(), e.Coin, e.Score, e.Bias, e.Price)
}

// loadScoreEvents returns the most recent events, oldest-first (matching the
// in-memory scoreLog convention; ScoreLog() reverses to newest-first).
func (db *DB) loadScoreEvents(limit int) []ScoreEvent {
	rows, err := db.sql.Query(`SELECT ts,coin,score,bias,price FROM score_events ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ScoreEvent
	for rows.Next() {
		var ts int64
		var e ScoreEvent
		if err := rows.Scan(&ts, &e.Coin, &e.Score, &e.Bias, &e.Price); err != nil {
			continue
		}
		e.Time = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (db *DB) upsertTrade(book string, t *PaperTrade) {
	var ct int64
	if t.CloseTime != nil {
		ct = t.CloseTime.UnixMilli()
	}
	db.sql.Exec(`INSERT INTO paper_trades
	  (id,book,coin,dir,score,entry,tp,sl,cur,pnl_pct,status,outcome,open_time,close_time,oi,cvd,funding,tp1,tp2,legs,filled,realized,be_hit,be_price)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON DUPLICATE KEY UPDATE
	    cur=VALUES(cur), pnl_pct=VALUES(pnl_pct), status=VALUES(status),
	    outcome=VALUES(outcome), close_time=VALUES(close_time), sl=VALUES(sl),
	    tp1=VALUES(tp1), tp2=VALUES(tp2),
	    legs=VALUES(legs), filled=VALUES(filled), realized=VALUES(realized),
	    be_hit=VALUES(be_hit), be_price=VALUES(be_price)`,
		t.ID, book, t.Coin, t.Dir, t.Score, t.Entry, t.TP, t.SL, t.Cur, t.PnLPct,
		t.Status, t.Outcome, t.OpenTime.UnixMilli(), ct, t.OI, t.CVD, t.Funding,
		t.TP1, t.TP2, t.Legs, t.Filled, t.Realized, t.BEHit, t.BEPrice)
}

func (db *DB) insertLiquidation(r LiqRow) {
	db.sql.Exec(`INSERT INTO liquidations(ts,coin,side,px,usd) VALUES(?,?,?,?,?)`,
		r.Time, r.Coin, r.Side, r.Px, r.USD)
}

// clearTrades deletes every simulated trade for one strategy book (admin reset).
func (db *DB) clearTrades(book string) { db.sql.Exec(`DELETE FROM paper_trades WHERE book=?`, book) }

// clearClosedTrades deletes only the CLOSED trades of a book, keeping open positions.
func (db *DB) clearClosedTrades(book string) {
	db.sql.Exec(`DELETE FROM paper_trades WHERE book=? AND status='closed'`, book)
}

func (db *DB) loadTrades(book string) []*PaperTrade {
	rows, err := db.sql.Query(`SELECT id,coin,dir,score,entry,tp,sl,cur,pnl_pct,status,outcome,open_time,close_time,oi,cvd,funding,tp1,tp2,legs,filled,realized,be_hit,be_price
	  FROM paper_trades WHERE book=? ORDER BY open_time ASC`, book)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*PaperTrade
	for rows.Next() {
		t := &PaperTrade{}
		var ot, ct int64
		if err := rows.Scan(&t.ID, &t.Coin, &t.Dir, &t.Score, &t.Entry, &t.TP, &t.SL,
			&t.Cur, &t.PnLPct, &t.Status, &t.Outcome, &ot, &ct, &t.OI, &t.CVD, &t.Funding,
			&t.TP1, &t.TP2, &t.Legs, &t.Filled, &t.Realized, &t.BEHit, &t.BEPrice); err != nil {
			continue
		}
		t.OpenTime = time.UnixMilli(ot).UTC()
		if ct > 0 {
			tt := time.UnixMilli(ct).UTC()
			t.CloseTime = &tt
		}
		out = append(out, t)
	}
	return out
}
