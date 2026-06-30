package cache

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// db.go persists the two things we want to verify across restarts: the score
// cross log (±20 signals) and the paper-trade books. NOT snapshots — this only
// durably stores the signals/trades already produced in memory.

const dbSchema = `
CREATE TABLE IF NOT EXISTS score_events (
  ts    INTEGER NOT NULL,
  coin  TEXT NOT NULL,
  score INTEGER,
  bias  TEXT,
  price REAL
);
CREATE INDEX IF NOT EXISTS idx_se_ts ON score_events(ts);

CREATE TABLE IF NOT EXISTS paper_trades (
  id         TEXT PRIMARY KEY,
  book       TEXT NOT NULL,
  coin       TEXT, dir TEXT, score INTEGER,
  entry      REAL, tp REAL, sl REAL, cur REAL, pnl_pct REAL,
  status     TEXT, outcome TEXT,
  open_time  INTEGER, close_time INTEGER,
  oi REAL DEFAULT 0, cvd REAL DEFAULT 0, funding REAL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_pt_open ON paper_trades(open_time);

CREATE TABLE IF NOT EXISTS orderbook_snaps (
  ts INTEGER NOT NULL, coin TEXT NOT NULL,
  mid REAL, bid_usd REAL, ask_usd REAL, imbal REAL,
  bid_wall REAL, bid_wall_usd REAL, ask_wall REAL, ask_wall_usd REAL
);
CREATE INDEX IF NOT EXISTS idx_ob_ts ON orderbook_snaps(ts);

CREATE TABLE IF NOT EXISTS liquidations (
  ts INTEGER NOT NULL, coin TEXT NOT NULL, side TEXT, px REAL, usd REAL
);
CREATE INDEX IF NOT EXISTS idx_liq_ts ON liquidations(ts);

CREATE TABLE IF NOT EXISTS users (
  username  TEXT PRIMARY KEY,
  pass_hash TEXT NOT NULL,
  role      TEXT NOT NULL DEFAULT 'member',
  status    TEXT NOT NULL DEFAULT 'active', -- active | pending | banned
  uid       TEXT DEFAULT '',
  created   INTEGER,
  expiry    INTEGER DEFAULT 0,             -- 0 = none
  notes     TEXT DEFAULT ''
);
`

// DB wraps the SQLite handle for persistence.
type DB struct{ sql *sql.DB }

func openDB(path string) (*DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1) // sqlite is single-writer
	if _, err := d.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		d.Close()
		return nil, err
	}
	if _, err := d.Exec(dbSchema); err != nil {
		d.Close()
		return nil, err
	}
	// migrations for older DBs (errors ignored if the column already exists)
	d.Exec("ALTER TABLE paper_trades ADD COLUMN oi REAL DEFAULT 0")
	d.Exec("ALTER TABLE paper_trades ADD COLUMN cvd REAL DEFAULT 0")
	d.Exec("ALTER TABLE paper_trades ADD COLUMN funding REAL DEFAULT 0")
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
}

func (db *DB) upsertUser(username, passHash, role, status string) {
	db.sql.Exec(`INSERT INTO users(username,pass_hash,role,status,created) VALUES(?,?,?,?,?)
	  ON CONFLICT(username) DO UPDATE SET role=excluded.role, status=excluded.status`,
		username, passHash, role, status, time.Now().UnixMilli())
}

// userAuth returns (passHash, role, status) for a username, ok=false if absent.
func (db *DB) userAuth(username string) (hash, role, status string, ok bool) {
	row := db.sql.QueryRow(`SELECT pass_hash,role,status FROM users WHERE username=?`, username)
	if err := row.Scan(&hash, &role, &status); err != nil {
		return "", "", "", false
	}
	return hash, role, status, true
}

func (db *DB) listUsers() []User {
	rows, err := db.sql.Query(`SELECT username,role,status,uid,created,expiry,notes FROM users ORDER BY created DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if rows.Scan(&u.Username, &u.Role, &u.Status, &u.UID, &u.Created, &u.Expiry, &u.Notes) == nil {
			out = append(out, u)
		}
	}
	return out
}

func (db *DB) setUserRole(username, role, status string) {
	db.sql.Exec(`UPDATE users SET role=?, status=? WHERE username=?`, role, status, username)
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
	  (id,book,coin,dir,score,entry,tp,sl,cur,pnl_pct,status,outcome,open_time,close_time,oi,cvd,funding)
	  VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET
	    cur=excluded.cur, pnl_pct=excluded.pnl_pct, status=excluded.status,
	    outcome=excluded.outcome, close_time=excluded.close_time, sl=excluded.sl`,
		t.ID, book, t.Coin, t.Dir, t.Score, t.Entry, t.TP, t.SL, t.Cur, t.PnLPct,
		t.Status, t.Outcome, t.OpenTime.UnixMilli(), ct, t.OI, t.CVD, t.Funding)
}

func (db *DB) insertOrderBook(t time.Time, r OrderBookRow) {
	db.sql.Exec(`INSERT INTO orderbook_snaps
	  (ts,coin,mid,bid_usd,ask_usd,imbal,bid_wall,bid_wall_usd,ask_wall,ask_wall_usd)
	  VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.UnixMilli(), r.Coin, r.Mid, r.BidUSD, r.AskUSD, r.Imbal,
		r.BidWall, r.BidWallU, r.AskWall, r.AskWallU)
}

func (db *DB) insertLiquidation(r LiqRow) {
	db.sql.Exec(`INSERT INTO liquidations(ts,coin,side,px,usd) VALUES(?,?,?,?,?)`,
		r.Time, r.Coin, r.Side, r.Px, r.USD)
}

func (db *DB) loadTrades(book string) []*PaperTrade {
	rows, err := db.sql.Query(`SELECT id,coin,dir,score,entry,tp,sl,cur,pnl_pct,status,outcome,open_time,close_time,oi,cvd,funding
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
			&t.Cur, &t.PnLPct, &t.Status, &t.Outcome, &ot, &ct, &t.OI, &t.CVD, &t.Funding); err != nil {
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
