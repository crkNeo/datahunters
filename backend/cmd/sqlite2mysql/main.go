// Command sqlite2mysql copies every row from the old SQLite datahunter.db into
// a MySQL database, preserving primary keys (so re-running is idempotent — rows
// that already exist are skipped via INSERT IGNORE).
//
// Usage:
//
//	sqlite2mysql -sqlite ./datahunter.db -mysql "user:pass@tcp(127.0.0.1:3306)/datahunter?charset=utf8mb4"
//
// The MySQL DSN may also be supplied via the MYSQL_DSN environment variable.
// The destination schema is created automatically (same DDL the server uses).
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"datahunter/internal/cache"

	_ "modernc.org/sqlite"
)

// tables are copied in FK-agnostic order (there are no FKs; order is cosmetic).
// push_subs is intentionally SKIPPED: the MySQL schema keys it by sha256(endpoint)
// ("id"), which the old SQLite table lacks — a generic column copy would be
// silently dropped by INSERT IGNORE anyway, and browsers re-register their push
// subscription automatically on next load.
var tables = []string{
	"score_events", "paper_trades", "liquidations",
	"users", "site_config", "articles",
}

func main() {
	src := flag.String("sqlite", "datahunter.db", "path to the source SQLite file")
	dsn := flag.String("mysql", os.Getenv("MYSQL_DSN"), "destination MySQL DSN (or set MYSQL_DSN)")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("no MySQL DSN: pass -mysql or set MYSQL_DSN")
	}
	if _, err := os.Stat(*src); err != nil {
		log.Fatalf("source SQLite not found: %v", err)
	}

	s, err := sql.Open("sqlite", *src)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer s.Close()

	d, err := cache.OpenMySQL(*dsn) // opens + creates the schema
	if err != nil {
		log.Fatalf("open mysql: %v", err)
	}
	defer d.Close()

	total := 0
	for _, t := range tables {
		n, err := copyTable(s, d, t)
		if err != nil {
			log.Fatalf("copy %s: %v", t, err)
		}
		total += n
		log.Printf("copied %-14s %d rows", t, n)
	}
	log.Printf("done: %d rows migrated into MySQL", total)
}

// copyTable streams every row of one table from src (SQLite) into dst (MySQL)
// using a column-generic INSERT IGNORE, so it works for any schema and skips
// rows whose primary key already exists on a re-run.
func copyTable(src, dst *sql.DB, table string) (int, error) {
	rows, err := src.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	stmt := fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)",
		table, strings.Join(cols, ","), ph)

	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		if _, err := dst.Exec(stmt, vals...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
