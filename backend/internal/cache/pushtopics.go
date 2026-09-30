package cache

import (
	"encoding/json"

	"datahunter/internal/auth"
)

// pushtopics.go: 使用者自選推播。每則使用者可見的通知歸一個 topic;使用者可在帳號頁
// 逐項/逐策略(訂單塊還逐週期)開關。偏好存成「明確選擇」map(prefs_json = {topic:bool}),
// 沒選過的 topic 用其預設值(目前全部預設開啟)。發送時 PushSendTopic 依「最低角色 + 使用者
// 的有效開關」過濾,所以每個角色只會收到自己權限內、且自己開著的類型。管理員專屬的操作
// 提醒(新註冊、VIP 申請、推薦獎勵)不走這裡。

// PushTopic (+On) 是回給前端的一筆:On 是「此使用者對該 topic 的有效開關」。
type PushTopic struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"` // "strategy" | "message"
	On    bool   `json:"on"`
}

type pushTopicDef struct {
	key, label, group, tab string
}

// 只列「真的會發使用者推播」的策略與消息。訂單塊(含 v2)兩個週期各自成一個 topic。
// 順序 = UI 顯示順序。
var pushTopicDefs = []pushTopicDef{
	// 策略類(一個開關管該策略/週期的開倉/平倉/止盈/保本)
	{"strat:paper", "星軌", "strategy", "paper"},
	{"strat:gamble", "超新星", "strategy", "gamble"},
	{"strat:emaonly", "銀河", "strategy", "emaonly"},
	{"strat:conv", "冥王星", "strategy", "conv"},
	{"strat:bollema", "海王星", "strategy", "bollema"},
	{"strat:pulsarv3", "脈衝星", "strategy", "pulsarv3"},
	{"strat:pulsar", "脈衝星(舊)", "strategy", "pulsar"},
	{"strat:pulsarv8", "脈衝星v8", "strategy", "pulsarv8"},
	{"strat:pulsarv9", "脈衝星v9", "strategy", "pulsarv9"},
	{"strat:orderblock", "訂單塊 1H", "strategy", "orderblock"},
	{"strat:orderblock_4h", "訂單塊 4H", "strategy", "orderblock"},
	{"strat:orderblockv2", "訂單塊v2 1H", "strategy", "orderblockv2"},
	{"strat:orderblockv2_4h", "訂單塊v2 4H", "strategy", "orderblockv2"},
	{"strat:srmtf", "反轉訊號", "strategy", "srmtf"},
	// 消息類(逐項)
	{"msg:macro", "整點大盤分析", "message", ""},
	{"msg:news", "市場快訊", "message", "news"},
	{"msg:sectors", "板塊輪動", "message", "sectors"},
	{"msg:sr", "支撐壓力", "message", "sr"},
	{"msg:whales", "名人動向", "message", "whales"},
	{"msg:polyscout", "聰明錢共識", "message", "polyscout"},
	{"msg:robinhood", "Robinhood上架", "message", "robinhood"},
	{"msg:upbit", "Upbit公告", "message", "upbit"},
}

// pushDefaultOff 是「預設關閉、需使用者自行開啟」的 topic。目前全部預設開啟,故為空;
// 保留此機制,日後若某類型太吵想預設關,加進來即可。
var pushDefaultOff = map[string]bool{}

var pushTopicByKey = func() map[string]pushTopicDef {
	m := make(map[string]pushTopicDef, len(pushTopicDefs))
	for _, t := range pushTopicDefs {
		m[t.key] = t
	}
	return m
}()

// stratTopic 由 book 名(含週期後綴)回傳其推播 topic key,與登錄表一致。
// main 對應分頁 paper;訂單塊 1h/4h 兩個週期各自一個 key。
func stratTopic(book string) string {
	if book == "main" {
		return "strat:paper"
	}
	return "strat:" + book
}

// topicOn 解析「某使用者對某 topic 的有效開關」:明確選過就用,否則用預設。
func topicOn(prefs map[string]bool, key string) bool {
	if v, ok := prefs[key]; ok {
		return v
	}
	return !pushDefaultOff[key]
}

// topicMinRole 解析一個 topic 的最低角色。空 Tab = 公開;未知 topic 失敗關閉(admin)。
func (s *Store) topicMinRole(topic string) string {
	def, ok := pushTopicByKey[topic]
	if !ok {
		return auth.RoleAdmin
	}
	if def.tab == "" {
		return auth.RolePublic
	}
	return s.TabRole(def.tab)
}

// PushTopicsFor 回傳某使用者「看得到」的 topic(依角色過濾)+ 其有效開關,順序照登錄表。
func (s *Store) PushTopicsFor(role, username string) []PushTopic {
	prefs := s.pushPrefs(username)
	out := make([]PushTopic, 0, len(pushTopicDefs))
	for _, t := range pushTopicDefs {
		if auth.AtLeast(role, s.topicMinRole(t.key)) {
			out = append(out, PushTopic{Key: t.key, Label: t.label, Group: t.group, On: topicOn(prefs, t.key)})
		}
	}
	return out
}

func validTopic(key string) bool { _, ok := pushTopicByKey[key]; return ok }

// PushSendTopic 發一則使用者可選的推播:只送給「active + 角色足夠 + 有效開關為開」的訂閱。
func (s *Store) PushSendTopic(topic, title, body, url string) {
	if s.pushMgr == nil || s.db == nil {
		return
	}
	subs := s.db.subsForTopic(topic, s.topicMinRole(topic))
	if len(subs) > 0 {
		s.pushMgr.SendTo(subs, title, body, url)
	}
}

// ---- DB layer ----

// subsForTopic 回傳應收到此 topic 的訂閱 JSON:active 使用者、角色 ≥ minRole、
// 且該使用者對此 topic 的有效開關為開。角色與開關判斷在 Go 端做。
func (db *DB) subsForTopic(topic, minRole string) []string {
	rows, err := db.sql.Query(`SELECT p.sub, u.role, COALESCE(pf.prefs_json,'')
	  FROM push_subs p
	  JOIN users u ON u.username = p.username
	  LEFT JOIN push_prefs pf ON pf.username = p.username
	  WHERE u.status = 'active'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub, role, prefsJSON string
		if rows.Scan(&sub, &role, &prefsJSON) != nil {
			continue
		}
		if !auth.AtLeast(role, minRole) || !topicOn(parsePrefs(prefsJSON), topic) {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// parsePrefs 解析 prefs_json(明確選擇 map);壞/空 JSON = 空 map(全部用預設)。
func parsePrefs(prefsJSON string) map[string]bool {
	if prefsJSON == "" {
		return nil
	}
	var m map[string]bool
	if json.Unmarshal([]byte(prefsJSON), &m) != nil {
		return nil
	}
	return m
}

func (db *DB) getPushPrefs(username string) string {
	var p string
	db.sql.QueryRow(`SELECT prefs_json FROM push_prefs WHERE username=?`, username).Scan(&p)
	return p
}

func (db *DB) setPushPrefs(username, prefsJSON string) {
	db.sql.Exec(`INSERT INTO push_prefs(username,prefs_json) VALUES(?,?)
	  ON DUPLICATE KEY UPDATE prefs_json=VALUES(prefs_json)`, username, prefsJSON)
}

// ---- Store API (for the HTTP layer) ----

// pushPrefs 回傳使用者的明確選擇 map(空 = 全部用預設)。
func (s *Store) pushPrefs(username string) map[string]bool {
	if s.db == nil || username == "" {
		return nil
	}
	return parsePrefs(s.db.getPushPrefs(username))
}

// SetPushPref 設定使用者對單一 topic 的開關(只接受已知 topic)。
func (s *Store) SetPushPref(username, key string, on bool) {
	if s.db == nil || username == "" || !validTopic(key) {
		return
	}
	prefs := s.pushPrefs(username)
	if prefs == nil {
		prefs = map[string]bool{}
	}
	prefs[key] = on
	blob, _ := json.Marshal(prefs)
	s.db.setPushPrefs(username, string(blob))
}
