package collector

import (
	"strings"
	"testing"
)

// Every column listed for migration must also exist in that table's CREATE
// statement. If the two drift, a fresh install and an upgraded one end up with
// different schemas — and the difference only surfaces later, as an insert
// failing on one machine and not the other.
func TestAddedColumnsMatchDDL(t *testing.T) {
	ddls := map[string]string{"pattern_hits": patternHitsDDL}
	for name, ddl := range tableDDL {
		ddls[name] = ddl
	}
	ddls["collector_state"] = stateDDL

	for table, cols := range addedColumns {
		ddl, ok := ddls[table]
		if !ok {
			t.Errorf("addedColumns 提到 %s,但找不到它的 CREATE 敘述", table)
			continue
		}
		for col, alter := range cols {
			if !strings.Contains(ddl, "\n  "+col+" ") {
				t.Errorf("%s.%s 在遷移清單裡,卻不在 CREATE 敘述中 —— 新安裝與升級安裝的結構會不一致", table, col)
			}
			if !strings.Contains(alter, col) {
				t.Errorf("%s.%s 的 ALTER 敘述沒有提到該欄位名稱: %q", table, col, alter)
			}
			if !strings.HasPrefix(alter, "ADD COLUMN") {
				t.Errorf("%s.%s 的遷移敘述應以 ADD COLUMN 開頭,得到 %q", table, col, alter)
			}
		}
	}
}
