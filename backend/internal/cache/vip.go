package cache

import (
	"fmt"
	"time"
)

// vip.go: 會員申請升級 VIP。會員在「我的推廣」點「申請VIP」,上傳入金證明 + 交易量
// 證明兩張圖 → 進審核佇列(vip_applications, 每人一列)。後台管理員看得到兩張圖,
// 通過即把該帳號升級為 vip;駁回則可重新申請。

// VIPApp is one VIP application (admin review row / member status).
type VIPApp struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Deposit  string `json:"deposit"` // 入金證明圖 URL
	Volume   string `json:"volume"`  // 交易量證明圖 URL
	Status   string `json:"status"`  // pending | approved | rejected
	Applied  int64  `json:"applied"`
	Reviewed int64  `json:"reviewed"`
}

// ---- DB layer ----

// vipUpsert 建立/覆蓋一筆申請。駁回或首次 → 回到 pending;審核時間清零。
func (db *DB) vipUpsert(username, deposit, volume string) error {
	_, err := db.sql.Exec(`INSERT INTO vip_applications(username,deposit_proof,volume_proof,status,applied,reviewed)
	  VALUES(?,?,?,'pending',?,0)
	  ON DUPLICATE KEY UPDATE deposit_proof=VALUES(deposit_proof), volume_proof=VALUES(volume_proof),
	    status='pending', applied=VALUES(applied), reviewed=0`,
		username, deposit, volume, time.Now().UnixMilli())
	return err
}

func (db *DB) vipOf(username string) (VIPApp, bool) {
	var a VIPApp
	err := db.sql.QueryRow(`SELECT id,username,deposit_proof,volume_proof,status,COALESCE(applied,0),COALESCE(reviewed,0)
	  FROM vip_applications WHERE username=? COLLATE utf8mb4_bin`, username).
		Scan(&a.ID, &a.Username, &a.Deposit, &a.Volume, &a.Status, &a.Applied, &a.Reviewed)
	return a, err == nil
}

func (db *DB) vipList() []VIPApp {
	// 待審核置頂,其次照申請時間新到舊。
	rows, err := db.sql.Query(`SELECT id,username,deposit_proof,volume_proof,status,COALESCE(applied,0),COALESCE(reviewed,0)
	  FROM vip_applications ORDER BY (status='pending') DESC, applied DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []VIPApp{}
	for rows.Next() {
		var a VIPApp
		if rows.Scan(&a.ID, &a.Username, &a.Deposit, &a.Volume, &a.Status, &a.Applied, &a.Reviewed) == nil {
			out = append(out, a)
		}
	}
	return out
}

// ---- Store API ----

// ApplyVIP 會員送出 VIP 申請(兩張證明圖的 URL 由 API 層存檔後傳入)。
// 已是 VIP/管理員的帳號不需申請;pending 中重送則覆蓋圖片。
func (s *Store) ApplyVIP(username, deposit, volume string) error {
	if s.db == nil {
		return fmt.Errorf("尚未啟用")
	}
	role, _, ok := s.db.userRoleStatus(username)
	if !ok {
		return fmt.Errorf("帳號不存在")
	}
	if role == "vip" || role == "admin" {
		return fmt.Errorf("你已經是 VIP,不需要再申請")
	}
	if deposit == "" || volume == "" {
		return fmt.Errorf("入金證明與交易量證明都要上傳")
	}
	if err := s.db.vipUpsert(username, deposit, volume); err != nil {
		return fmt.Errorf("送出失敗,請稍後再試")
	}
	s.NotifyVIPApply(username)
	return nil
}

// VIPStatus 回會員自己的申請狀態("" = 尚未申請)。
func (s *Store) VIPStatus(username string) VIPApp {
	if s.db == nil {
		return VIPApp{}
	}
	if a, ok := s.db.vipOf(username); ok {
		return a
	}
	return VIPApp{}
}

// VIPApplications 回全部申請(後台審核用)。
func (s *Store) VIPApplications() []VIPApp {
	if s.db == nil {
		return []VIPApp{}
	}
	if l := s.db.vipList(); l != nil {
		return l
	}
	return []VIPApp{}
}

// ReviewVIP 審核一筆申請:approve → 升級該帳號為 vip;reject → 標記駁回(會員可重新申請)。
func (s *Store) ReviewVIP(id int64, approve bool) bool {
	if s.db == nil {
		return false
	}
	var username, status string
	if s.db.sql.QueryRow(`SELECT username,status FROM vip_applications WHERE id=?`, id).Scan(&username, &status) != nil {
		return false
	}
	newStatus := "rejected"
	if approve {
		newStatus = "approved"
	}
	res, err := s.db.sql.Exec(`UPDATE vip_applications SET status=?, reviewed=? WHERE id=?`,
		newStatus, time.Now().UnixMilli(), id)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false
	}
	if approve {
		// 只升級「還是會員」的帳號,避免把管理員誤降或重複動作。
		if role, st, ok := s.db.userRoleStatus(username); ok && role == "member" {
			s.db.setUserRole(username, "vip", st)
		}
	}
	return true
}

// NotifyVIPApply 通知管理員有新的 VIP 申請待審(同新用戶註冊的通道)。
func (s *Store) NotifyVIPApply(username string) {
	if s.notifier != nil && s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("⭐ <b>VIP 申請</b>\n帳號 %s\n請至後台審核入金/交易量證明", username))
	}
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			go s.pushMgr.SendTo(subs, "⭐ VIP 申請", username+" 申請升級 VIP,待審核", "/")
		}
	}
}
