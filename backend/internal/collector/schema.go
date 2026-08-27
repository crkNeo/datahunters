package collector

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// schema.go owns the DDL for the research tables and their daily partitions.
//
// Design rules these tables follow, and why:
//
//  1. RAW ONLY. Nothing derived is stored — no z-scores, no percentiles, no
//     "fuel score". Derived values are computed offline from this history with
//     a strictly backward-looking window. Storing a derived value freezes the
//     formula and, worse, invites a rewrite that quietly uses full-history
//     statistics — which is lookahead, and it makes any later backtest lie.
//
//  2. EVERY symbol in the universe, every minute — not just the interesting
//     ones. A log of only the coins that fired can tell you how often a signal
//     preceded a move; it can never tell you how often the move happened
//     WITHOUT the signal. Without that control group there is no lift, and
//     without lift there is no way to know whether a score has any information
//     in it at all.
//
//  3. PARTITION BY day from the start. On MySQL, deleting a slice of a large
//     time-series table with DELETE means row-by-row work, a mountain of undo
//     log, and space that never returns to the filesystem. With daily RANGE
//     partitions, retention is DROP PARTITION — near-instant, and the space is
//     actually freed. Retrofitting partitions onto a table that already holds
//     tens of millions of rows means a full table rebuild, so it has to be done
//     at creation time.
//
// The `day` column (YYYYMMDD, UTC) is redundant with `ts` on purpose: MySQL
// requires every column of a partitioning expression to appear in every unique
// key, and partitioning on a raw ms timestamp would need an unwieldy range list.
// It also lets queries prune partitions with a plain `WHERE day = ?`.

// partitionedTables are rebuilt daily and pruned by retention.
//
// store-free「爆發型態」:偵測改吃記憶體滾動窗、結果回填改抓即時 K 線,collector 已不再
// 寫任何逐分鐘快照,所以沒有分區表了。唯一持久化的是 pattern_hits(訊號+結果)。
// depth_1m / spot_1m / labels_1m / regime_1m / snap_1m / universe_1d / unlock_* 皆已移除;
// 若日後要恢復完整採集,需還原 pattern.go/collector.go/schema.go 與 run.sh。
var partitionedTables = []string{}

// tableDDL previously held the per-minute snapshot tables; store-free left it empty.
// ensureSchema still ranges it (a no-op now) so restoring a table is a one-line add.
var tableDDL = map[string]string{}

// collector_state is a tiny key/value table for progress markers.
//
// The labeler's position CANNOT be derived from MAX(labels_1m.ts): a stretch of
// time with no snapshots (the collector was down, or restarted) produces zero
// labels, so a derived watermark would never advance past the gap and the
// labeler would re-read the same empty window forever. An explicit marker
// advances whether or not the window produced rows.
const stateDDL = `CREATE TABLE IF NOT EXISTS collector_state (
  k VARCHAR(64) NOT NULL,
  v BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (k)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// universe_1d is small (one row per symbol per day) and never needs pruning, so
// it stays unpartitioned. rank_vol is the 24h-turnover rank at selection time —
// recording it means a later analysis can ask whether lift is concentrated in a
// particular liquidity band.
const universeDDL = `CREATE TABLE IF NOT EXISTS universe_1d (
  day           INT    NOT NULL,
  symbol        VARCHAR(32) NOT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT '',
  onboard_ts    BIGINT NOT NULL DEFAULT 0,
  quote_vol_24h DOUBLE NOT NULL DEFAULT 0,
  rank_vol      INT    NOT NULL DEFAULT 0,
  selected      TINYINT NOT NULL DEFAULT 0,
  PRIMARY KEY (day, symbol)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// addedColumns lists columns introduced after a table first shipped.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a
// column added to a DDL never reaches an installation running an earlier build.
// Every INSERT then fails on the missing column while the process keeps going —
// the write path logs once a minute and the table simply stops growing, which
// looks exactly like nothing having happened. That is the worst failure shape
// available: silent, and indistinguishable from a normal quiet period.
//
// Same idempotent information_schema + ALTER approach cache/db.go uses. Any new
// column added to a DDL above must also be listed here.
var addedColumns = map[string]map[string]string{
	"pattern_hits": {
		"mkt_ret": "ADD COLUMN mkt_ret DOUBLE NOT NULL DEFAULT 0",
	},
}

// ensureColumns applies addedColumns. Safe to call on every start.
func ensureColumns(db *sql.DB) error {
	for table, cols := range addedColumns {
		for col, ddl := range cols {
			var has int
			if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
			     WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?`,
				table, col).Scan(&has); err != nil {
				return fmt.Errorf("check %s.%s: %w", table, col, err)
			}
			if has > 0 {
				continue
			}
			if _, err := db.Exec("ALTER TABLE " + table + " " + ddl); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, col, err)
			}
			log.Printf("collector: 已補上 %s.%s 欄位", table, col)
		}
	}
	return nil
}

// dayKey renders a UTC time as the YYYYMMDD partition key.
func dayKey(t time.Time) int {
	t = t.UTC()
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}

// dayKeyMs is dayKey for a unix-ms timestamp.
func dayKeyMs(ms int64) int { return dayKey(time.UnixMilli(ms).UTC()) }

// ensureSchema creates the research tables if absent and makes sure partitions
// exist for the days about to be written. Safe to call repeatedly.
func ensureSchema(db *sql.DB, retentionDays int) error {
	for _, name := range partitionedTables {
		ddl, ok := tableDDL[name]
		if !ok {
			return fmt.Errorf("no DDL for %s", name)
		}
		// A brand-new table starts with a single sentinel partition; real days
		// are appended by ensurePartitions below. Partitioning can be disabled
		// server-side, so fall back to an ordinary table rather than refusing
		// to start — collecting unpartitioned data beats collecting none.
		partitioned := ddl + "\nPARTITION BY RANGE (day) (PARTITION p_start VALUES LESS THAN (20000101))"
		if _, err := db.Exec(partitioned); err != nil {
			log.Printf("collector: %s partitioned create failed (%v) — falling back to unpartitioned", name, err)
			if _, err2 := db.Exec(ddl); err2 != nil {
				return fmt.Errorf("create %s: %w", name, err2)
			}
		}
	}
	if _, err := db.Exec(stateDDL); err != nil {
		return fmt.Errorf("create collector_state: %w", err)
	}
	if err := ensurePatternSchema(db); err != nil {
		return err
	}
	if err := ensureColumns(db); err != nil {
		return err
	}
	return ensurePartitions(db, retentionDays)
}

// ensurePartitions adds partitions for today through today+2 (so a write at a
// UTC day boundary never lands with nowhere to go) and drops those older than
// the retention window. Tables that ended up unpartitioned are skipped.
func ensurePartitions(db *sql.DB, retentionDays int) error {
	now := time.Now().UTC()
	for _, name := range partitionedTables {
		have, err := existingPartitions(db, name)
		if err != nil {
			return err
		}
		if len(have) == 0 {
			continue // unpartitioned fallback — nothing to maintain
		}
		for i := 0; i <= 2; i++ {
			d := now.AddDate(0, 0, i)
			key := dayKey(d)
			pname := fmt.Sprintf("p%d", key)
			if have[pname] {
				continue
			}
			next := dayKey(d.AddDate(0, 0, 1))
			stmt := fmt.Sprintf("ALTER TABLE %s ADD PARTITION (PARTITION %s VALUES LESS THAN (%d))", name, pname, next)
			if _, err := db.Exec(stmt); err != nil {
				// Losing one ADD PARTITION is not fatal on its own — but every
				// later day would land in no partition at all, so surface it.
				log.Printf("collector: add partition %s.%s: %v", name, pname, err)
			}
		}
		if retentionDays > 0 {
			cutoff := dayKey(now.AddDate(0, 0, -retentionDays))
			for pname := range have {
				if pname == "p_start" || !strings.HasPrefix(pname, "p") {
					continue
				}
				var key int
				if _, err := fmt.Sscanf(pname, "p%d", &key); err != nil || key == 0 {
					continue
				}
				if key < cutoff {
					if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s DROP PARTITION %s", name, pname)); err != nil {
						log.Printf("collector: drop partition %s.%s: %v", name, pname, err)
					} else {
						log.Printf("collector: dropped %s.%s (retention %dd)", name, pname, retentionDays)
					}
				}
			}
		}
	}
	return nil
}

func existingPartitions(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT PARTITION_NAME FROM information_schema.PARTITIONS
	                       WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND PARTITION_NAME IS NOT NULL`, table)
	if err != nil {
		return nil, fmt.Errorf("list partitions %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out[n] = true
		}
	}
	return out, rows.Err()
}
