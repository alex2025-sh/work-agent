// Package seed installs demo members, demo CRM/VIP data and the two VIP task
// types from the design doc, configured and published through the normal
// draft → publish → enable path (so the seed also exercises every check).
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/task"
)

type member struct{ id, name, team, role, open string }

var members = []member{
	{"rm_a", "RM 甲", "VIP_RM", "rm", "ou_rm_a"},
	{"rm_b", "RM 乙", "VIP_RM", "rm", "ou_rm_b"},
	{"rm_c", "RM 丙", "VIP_RM", "rm", "fail_ou_rm_c"}, // Lark always fails: demo of 投递失败
	{"leader_l", "Leader 林", "VIP_RM", "leader", "ou_leader_l"},
	{"cfg_c", "配置人员 陈", "VIP_RM", "config", ""},
	{"admin", "管理员", "VIP_RM", "admin", ""},
}

// Empty reports whether no task type exists yet.
func Empty(ctx context.Context, conn sqlx.SqlConn) bool {
	var n int64
	_ = conn.QueryRowCtx(ctx, &n, "SELECT COUNT(1) FROM bw_task_type")
	return n == 0
}

// Run seeds everything. now is the (possibly dev-shifted) current time.
func Run(ctx context.Context, conn sqlx.SqlConn, svc *task.Service, now time.Time) error {
	for _, m := range members {
		if _, err := conn.ExecCtx(ctx, "INSERT INTO bw_team_member (user_id, name, team, role, lark_open_id, active) VALUES (?,?,?,?,?,1)", m.id, m.name, m.team, m.role, m.open); err != nil {
			return err
		}
	}
	if err := SeedCases(ctx, conn, now); err != nil {
		return err
	}
	for _, def := range []typeDef{caseCreated(), ongoingReview()} {
		if err := install(ctx, svc, def); err != nil {
			return fmt.Errorf("%s: %w", def.req.TypeKey, err)
		}
	}
	return nil
}

// SeedCases inserts demo CRM cases and VIP profiles relative to now.
func SeedCases(ctx context.Context, conn sqlx.SqlConn, now time.Time) error {
	ms := func(d time.Duration) int64 { return clock.Ms(now.Add(-d)) }
	h := time.Hour
	vips := []struct {
		uid, level, source, profit, rm string
		vip                            int
	}{
		{"8812003390", "VIP 3", "交易量", "182000.5", "rm_a", 1},
		{"7745000218", "VIP 2", "资产", "56000", "rm_b", 1},
		{"6630007741", "VIP 4", "邀请", "910300", "rm_a", 1},
		{"5521000093", "VIP 1", "交易量", "12000", "", 1}, // no RM mapping
		{"4410009921", "", "", "0", "", 0},             // not VIP
		{"3390001177", "VIP 2", "资产", "33000", "rm_c", 1},
	}
	for _, v := range vips {
		if _, err := conn.ExecCtx(ctx, "INSERT INTO demo_vip (uid, is_vip, level, source, six_month_profit, rm_user) VALUES (?,?,?,?,?,?)", v.uid, v.vip, v.level, v.source, v.profit, v.rm); err != nil {
			return err
		}
	}
	cases := []struct {
		id, uid, typ, stage, entry string
		created, entered           int64
	}{
		{"AP-20261013-0532", "7745000218", "withdrawal_appeal", "first_review", "", ms(2 * h), 0},
		{"AP-20261010-0288", "6630007741", "account_appeal", "first_review", "", ms(80 * h), 0},
		{"AP-20261009-0204", "7745000218", "trading_appeal", "maker_review", "MR-0204-1", ms(110 * h), ms(72 * h)},
		{"AP-20261006-0417", "8812003390", "withdrawal_appeal", "maker_review", "MR-0417-1", ms(9 * 24 * h), ms(7*24*h + 30*time.Minute)},
		{"AP-20261011-0391", "8812003390", "account_appeal", "maker_review", "MR-0391-1", ms(60 * h), ms(50 * h)},
		{"AP-20261013-0551", "5521000093", "withdrawal_appeal", "maker_review", "MR-0551-1", ms(70 * h), ms(49 * h)},
		{"AP-20261012-0777", "4410009921", "withdrawal_appeal", "maker_review", "MR-0777-1", ms(90 * h), ms(60 * h)},
		{"AP-20261012-0640", "3390001177", "trading_appeal", "first_review", "", ms(1 * h), 0},
	}
	for _, c := range cases {
		if _, err := conn.ExecCtx(ctx, "INSERT INTO demo_case (appeal_id, uid, case_type, status, stage, stage_entered_at, maker_review_entry_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
			c.id, c.uid, c.typ, "open", c.stage, c.entered, c.entry, c.created, c.created); err != nil {
			return err
		}
	}
	return nil
}

type typeDef struct {
	req    task.CreateTypeReq
	spec   string
	graph  string
	layout string
	cards  string
}

func install(ctx context.Context, svc *task.Service, d typeDef) error {
	actor := "cfg_c"
	if _, err := svc.CreateType(ctx, actor, d.req); err != nil {
		return err
	}
	var spec task.ConfigSpec
	if err := json.Unmarshal([]byte(d.spec), &spec); err != nil {
		return fmt.Errorf("spec: %w", err)
	}
	if _, err := svc.SaveConfigDraft(ctx, actor, d.req.TypeKey, spec, "初始版本"); err != nil {
		return err
	}
	var fr task.FlowDraftReq
	if err := json.Unmarshal([]byte(d.graph), &fr.Graph); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	fr.Layout, fr.Cards, fr.Note = json.RawMessage(d.layout), json.RawMessage(d.cards), "初始版本"
	if _, err := svc.SaveFlowDraft(ctx, actor, d.req.TypeKey, fr); err != nil {
		return err
	}
	if p, err := svc.Publish(ctx, actor, d.req.TypeKey, task.PublishReq{Scope: "both", Rescan: "continue"}); err != nil {
		b, _ := json.MarshalIndent(p, "", "  ")
		return fmt.Errorf("publish: %w\n%s", err, b)
	}
	_, err := svc.ChangeStatus(ctx, actor, d.req.TypeKey, task.StatusReq{Action: "enable", Start: "duration", Duration: "30d"})
	return err
}

const targetTypes = `["withdrawal_appeal","account_appeal","trading_appeal"]`

const vipFields = `
  {"column":"is_vip","field":"vip.is_vip","type":"bool","required":false,"sensitivity":"normal"},
  {"column":"vip_level","field":"vip.level","type":"string","required":false,"sensitivity":"normal"},
  {"column":"vip_source","field":"vip.source","type":"string","required":false,"sensitivity":"normal"},
  {"column":"six_month_profit","field":"vip.six_month_profit","type":"number","required":false,"sensitivity":"sensitive"}`

// 需求一：Case Created 后 RM 跟进
func caseCreated() typeDef {
	return typeDef{
		req: task.CreateTypeReq{TypeKey: "VIP_CASE_CREATED", Name: "VIP Case 已创建", Team: "VIP_RM", ObjectType: "CRM Case",
			Description: "VIP 用户的目标类型 Case 创建后，提醒 RM 跟进；可延迟到 Case 创建 + 3 天后复查", DedupeFields: []string{"case.appeal_id"}},
		spec: `{
 "trigger":{"sources":["push","sql"],"datasource":"demo","push_event":"case.created",
   "query":"SELECT c.appeal_id, c.uid, c.case_type, c.status, c.created_at, v.is_vip, v.level AS vip_level, v.source AS vip_source, v.six_month_profit\nFROM demo_case c JOIN demo_vip v ON v.uid = c.uid\nWHERE c.created_at >= {{scan_from}} AND c.status = 'open' AND c.stage = 'first_review'"},
 "fields":[
  {"column":"appeal_id","field":"case.appeal_id","type":"string","required":true,"sensitivity":"normal"},
  {"column":"uid","field":"case.uid","type":"string","required":true,"sensitivity":"normal"},
  {"column":"case_type","field":"case.case_type","type":"string","required":true,"sensitivity":"normal"},
  {"column":"status","field":"case.status","type":"string","required":true,"sensitivity":"normal"},
  {"column":"created_at","field":"case.created_at","type":"datetime","required":true,"sensitivity":"normal"},` + vipFields + `],
 "eligibility":{"all":[
  {"field":"vip.is_vip","op":"is_true","label":"用户是 VIP"},
  {"field":"case.case_type","op":"in","value":` + targetTypes + `,"label":"Case Type 在目标范围"},
  {"field":"case.status","op":"eq","value":"open","label":"Case 未关闭"}]},
 "live_refresh":[{"capability":"data.case.get","args":{"appeal_id":"{{case.appeal_id}}"}},{"capability":"data.vip.profile","args":{"uid":"{{case.uid}}"}}],
 "invalidation":{"events":["case.closed"],"recheck_every":"6h"},
 "schedule":{"cron":"*/10 * * * *","timezone":"UTC","enabled":true},
 "max_per_run":200}`,
		graph: `{"nodes":[
 {"key":"start","type":"start","name":"开始 · 接收任务数据"},
 {"key":"fetch","type":"data","name":"取数 · VIP 档案","config":{"capability":"data.vip.profile","args":{"uid":"{{case.uid}}"},"on_error":"retry","retries":3,"backoff":"5m"}},
 {"key":"rm_follow","type":"human","name":"RM 跟进","config":{
   "assignee":{"team":"VIP_RM","rule":"VIP_UID_TO_RM","uid_field":"case.uid"},
   "card":"vip_case_card","round_label":"新建","later_title":"VIP Case 复查提醒","later_round_label":"T+3",
   "buttons":[{"key":"close","label":"Closed","kind":"complete","exit":"closed"},{"key":"delay","label":"延迟处理","kind":"delay"}],
   "delay":{"remind_at":"case.created_at + 3d","max":1,"revalidate":true}}},
 {"key":"end","type":"end","name":"结束 · 处理完成","config":{"close_reason":"completed"}}],
 "edges":[{"from":"start","exit":"next","to":"fetch"},{"from":"fetch","exit":"next","to":"rm_follow"},{"from":"rm_follow","exit":"closed","to":"end"}]}`,
		layout: `{"start":{"x":0,"y":0},"fetch":{"x":0,"y":130},"rm_follow":{"x":0,"y":260},"end":{"x":0,"y":420}}`,
		cards: `{"vip_case_card":{"name":"VIP Case 跟进卡","title":"VIP Case 已创建",
 "fields":[
  {"field":"case.appeal_id","label":"Appeal ID","format":"mono","lark":true,"list":true},
  {"field":"case.case_type","label":"Case Type","format":"text","lark":true,"list":false},
  {"field":"case.uid","label":"User ID","format":"mask","lark":false,"list":true},
  {"field":"vip.level","label":"VIP Level","format":"text","lark":true,"list":false},
  {"field":"vip.source","label":"VIP Source","format":"text","lark":false,"list":false},
  {"field":"vip.six_month_profit","label":"近 6 个月盈利","format":"money","lark":true,"list":false},
  {"field":"case.created_at","label":"Case 创建时间","format":"datetime","lark":false,"list":false}],
 "explain":true,"ai":{"enabled":false,"prompt_key":"bywork.card.summary","version":"v2"},
 "buttons":{"close":{"label":"Closed","style":"primary","confirm":false,"note_required":false},"delay":{"label":"延迟处理","style":"default"}},
 "links":[{"label":"在 CRM 中查看 Case","url":"https://crm.example.com/case/{{case.appeal_id}}"}],
 "result_text":"已 {{action.label}} · {{actor}} · {{time}}","waiting_text":"延迟中：{{next.time}} 会复查 Case 并在这张卡上再次提醒","lark_max_fields":4}}`,
	}
}

// 需求二：Maker Review 超时提醒（方案 A：48h 与 7d 在同一个任务内）
func ongoingReview() typeDef {
	btns := `"buttons":[{"key":"escalate","label":"升级并打开 Lark","kind":"external","url":"https://lark.example.com/approval/vip-escalation?appeal_id={{case.appeal_id}}","complete_round":true,"exit":"done"},{"key":"close","label":"Closed","kind":"complete","exit":"done"}]`
	return typeDef{
		req: task.CreateTypeReq{TypeKey: "VIP_ONGOING_REVIEW", Name: "VIP 审核持续提醒", Team: "VIP_RM", ObjectType: "CRM Case",
			Description: "VIP Case 在 Maker Review 停留 ≥48h 提醒 RM；满 7 天仍在则刷新同一张卡片", DedupeFields: []string{"case.appeal_id"}, RoundField: "case.maker_review_entry_id"},
		spec: `{
 "trigger":{"sources":["sql"],"datasource":"demo",
   "query":"SELECT c.appeal_id, c.uid, c.case_type, c.stage, c.stage_entered_at, c.maker_review_entry_id, v.is_vip, v.level AS vip_level, v.source AS vip_source, v.six_month_profit\nFROM demo_case c JOIN demo_vip v ON v.uid = c.uid\nWHERE c.stage = 'maker_review' AND c.status = 'open' AND c.stage_entered_at >= {{scan_from}}"},
 "fields":[
  {"column":"appeal_id","field":"case.appeal_id","type":"string","required":true,"sensitivity":"normal"},
  {"column":"uid","field":"case.uid","type":"string","required":true,"sensitivity":"normal"},
  {"column":"case_type","field":"case.case_type","type":"string","required":true,"sensitivity":"normal"},
  {"column":"stage","field":"case.stage","type":"string","required":true,"sensitivity":"normal"},
  {"column":"stage_entered_at","field":"case.stage_entered_at","type":"datetime","required":true,"sensitivity":"normal"},
  {"column":"maker_review_entry_id","field":"case.maker_review_entry_id","type":"string","required":true,"sensitivity":"normal"},` + vipFields + `],
 "eligibility":{"all":[
  {"field":"vip.is_vip","op":"is_true","label":"用户是 VIP"},
  {"field":"case.case_type","op":"in","value":` + targetTypes + `,"label":"目标 Case 类型"},
  {"field":"case.stage","op":"eq","value":"maker_review","label":"当前处于 Maker Review"},
  {"field":"case.stage_entered_at","op":"elapsed_gte","value":"48h","label":"Maker Review ≥ 48h"}]},
 "live_refresh":[{"capability":"data.case.get","args":{"appeal_id":"{{case.appeal_id}}"}}],
 "invalidation":{"events":["case.closed"],"recheck_every":"6h"},
 "schedule":{"cron":"*/5 * * * *","timezone":"UTC","enabled":true},
 "max_per_run":200}`,
		graph: `{"nodes":[
 {"key":"start","type":"start","name":"开始 · 接收任务数据"},
 {"key":"fetch_rm","type":"data","name":"解析当前 RM","config":{"capability":"data.rm.resolve","args":{"uid":"{{case.uid}}","team":"VIP_RM"},"on_error":"suspend"}},
 {"key":"is_7d","type":"condition","name":"已满 7 天？","config":{"branches":[{"exit":"yes","label":"已满 7 天","when":{"all":[{"field":"case.stage_entered_at","op":"elapsed_gte","value":"7d","label":"Maker Review ≥ 7 天"}]}}],"default_exit":"no"}},
 {"key":"rm_48h","type":"human","name":"RM 48 小时提醒","config":{
   "assignee":{"team":"VIP_RM","rule":"VIP_UID_TO_RM","uid_field":"case.uid"},
   "card":"vip_review_card","title":"VIP Case 审核提醒 · 48 小时","round_label":"48h",` + btns + `,
   "deadline":{"at":"case.stage_entered_at + 7d","exit":"timeout"}}},
 {"key":"wait_7d","type":"wait","name":"等到进入 Maker Review 满 7 天","config":{"until":"case.stage_entered_at + 7d"}},
 {"key":"refresh","type":"data","name":"刷新 Case 状态","config":{"capability":"data.case.get","args":{"appeal_id":"{{case.appeal_id}}"},"on_error":"retry","retries":3,"backoff":"5m"}},
 {"key":"still","type":"condition","name":"Case 仍在 Maker Review？","config":{"branches":[{"exit":"yes","label":"仍在 Maker Review","when":{"all":[{"field":"case.stage","op":"eq","value":"maker_review"},{"field":"case.status","op":"eq","value":"open"}]}}],"default_exit":"no"}},
 {"key":"rm_7d","type":"human","name":"RM 7 天提醒","config":{
   "assignee":{"team":"VIP_RM","rule":"VIP_UID_TO_RM","uid_field":"case.uid"},
   "card":"vip_review_card","title":"VIP Case 审核提醒 · 7 天","round_label":"7 天",` + btns + `}},
 {"key":"end_done","type":"end","name":"结束 · 处理完成","config":{"close_reason":"completed"}},
 {"key":"end_invalid","type":"end","name":"结束 · Case 已失效","config":{"close_reason":"invalidated"}}],
 "edges":[
  {"from":"start","exit":"next","to":"fetch_rm"},{"from":"fetch_rm","exit":"next","to":"is_7d"},
  {"from":"is_7d","exit":"no","to":"rm_48h"},{"from":"is_7d","exit":"yes","to":"rm_7d"},
  {"from":"rm_48h","exit":"done","to":"wait_7d"},{"from":"rm_48h","exit":"timeout","to":"refresh"},
  {"from":"wait_7d","exit":"next","to":"refresh"},{"from":"refresh","exit":"next","to":"still"},
  {"from":"still","exit":"yes","to":"rm_7d"},{"from":"still","exit":"no","to":"end_invalid"},
  {"from":"rm_7d","exit":"done","to":"end_done"}]}`,
		layout: `{"start":{"x":160,"y":0},"fetch_rm":{"x":160,"y":120},"is_7d":{"x":160,"y":240},"rm_48h":{"x":0,"y":380},"wait_7d":{"x":0,"y":540},"refresh":{"x":0,"y":670},"still":{"x":0,"y":800},"rm_7d":{"x":360,"y":940},"end_done":{"x":360,"y":1090},"end_invalid":{"x":0,"y":960}}`,
		cards: `{"vip_review_card":{"name":"VIP 审核提醒卡","title":"VIP Case 审核提醒 · {{node.round_label}}",
 "fields":[
  {"field":"case.appeal_id","label":"Appeal ID","format":"mono","lark":true,"list":true},
  {"field":"case.case_type","label":"Case Type","format":"text","lark":true,"list":false},
  {"field":"case.uid","label":"User ID","format":"mask","lark":false,"list":true},
  {"field":"vip.level","label":"VIP Level","format":"text","lark":true,"list":false},
  {"field":"vip.source","label":"VIP Source","format":"text","lark":false,"list":false},
  {"field":"vip.six_month_profit","label":"近 6 个月盈利","format":"money","lark":true,"list":false},
  {"field":"case.stage_entered_at","label":"进入 Maker Review","format":"datetime","lark":false,"list":false}],
 "explain":true,"ai":{"enabled":false,"prompt_key":"bywork.card.summary","version":"v2"},
 "buttons":{"escalate":{"label":"升级并打开 Lark","style":"primary","confirm":true},"close":{"label":"Closed","style":"default","confirm":true}},
 "links":[{"label":"在 CRM 中查看 Case","url":"https://crm.example.com/case/{{case.appeal_id}}"}],
 "result_text":"{{action.label}} · {{actor}} {{time}}","waiting_text":"{{next.time}} 如仍在 Maker Review，会在这张卡上再次提醒",
 "refreshed_text":"卡片已刷新到最新一轮","lark_max_fields":4}}`,
	}
}
