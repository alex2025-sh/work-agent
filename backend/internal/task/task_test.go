package task_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/seed"
	"bywork/internal/task"
)

type env struct {
	t      *testing.T
	ctx    context.Context
	conn   sqlx.SqlConn
	clk    *clock.Fixed
	svc    *task.Service
	eng    *engine.Engine
	sender *task.LogSender
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logx.Disable()
	raw, conn, err := db.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFixed(time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC))
	sources := datasrc.NewSources(5*time.Second, 5000)
	sources.Add("demo", raw)
	caps := datasrc.NewRegistry()
	datasrc.RegisterDemo(caps)
	eng := engine.New(conn, clk, "test")
	sender := task.NewLogSender()
	svc := task.New(conn, eng, caps, sources, clk, sender, task.Options{Owner: "test", LinkDomains: []string{"lark.example.com", "crm.example.com"},
		SignKey: "k", WebBaseURL: "http://x", DisplayTimezone: "UTC", ScanBudget: 10 * time.Second, OutboxMaxTry: 3})
	e := &env{t: t, ctx: context.Background(), conn: conn, clk: clk, svc: svc, eng: eng, sender: sender}
	if err := seed.Run(e.ctx, conn, svc, clk.Now()); err != nil {
		t.Fatal(err)
	}
	return e
}

// tick runs the three heartbeats once.
func (e *env) tick() {
	e.t.Helper()
	if _, err := e.svc.TriggerTick(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.eng.TimerTick(e.ctx, 10*time.Second); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.OutboxTick(e.ctx, 10*time.Second); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) advance(d time.Duration) {
	e.clk.Advance(d)
	e.tick()
}

func (e *env) task(typ, appeal string) *task.Task {
	e.t.Helper()
	var ts []task.Task
	if err := e.conn.QueryRowsCtx(e.ctx, &ts, "SELECT * FROM bw_task WHERE type_key=? AND object_key=? AND is_shadow=0 ORDER BY id", typ, appeal); err != nil {
		e.t.Fatal(err)
	}
	if len(ts) != 1 {
		e.t.Fatalf("%s %s: want 1 task, got %d", typ, appeal, len(ts))
	}
	return &ts[0]
}

func (e *env) reload(t *task.Task) *task.Task {
	e.t.Helper()
	var x task.Task
	if err := e.conn.QueryRowCtx(e.ctx, &x, "SELECT * FROM bw_task WHERE id=?", t.ID); err != nil {
		e.t.Fatal(err)
	}
	return &x
}

func (e *env) active(t *task.Task) *task.WorkItem {
	e.t.Helper()
	var w task.WorkItem
	if err := e.conn.QueryRowCtx(e.ctx, &w, "SELECT * FROM bw_work_item WHERE task_id=? AND status='active'", t.ID); err != nil {
		e.t.Fatalf("task %d has no active work item: %v", t.ID, err)
	}
	return &w
}

func (e *env) count(q string, args ...any) int64 {
	var n int64
	if err := e.conn.QueryRowCtx(e.ctx, &n, q, args...); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func viewer(u string) task.Viewer { return task.Viewer{UserID: u, Role: "rm", Team: "VIP_RM"} }

func (e *env) click(t *task.Task, user, button, key string) (*engine.EventResult, error) {
	w := e.active(t)
	return e.svc.Act(e.ctx, viewer(user), t.ID, task.ActionReq{WorkItemID: w.ID, Button: button, RequestKey: key, CardRev: w.CardRev})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestSeedPublishesAndScans(t *testing.T) {
	e := newEnv(t)
	e.tick()
	// eligible maker-review cases → one task each; the non-VIP case is skipped
	if n := e.count("SELECT COUNT(1) FROM bw_task WHERE type_key='VIP_ONGOING_REVIEW'"); n != 4 {
		t.Fatalf("ongoing review tasks = %d, want 4", n)
	}
	if n := e.count("SELECT COUNT(1) FROM bw_task WHERE type_key='VIP_CASE_CREATED'"); n != 3 {
		t.Fatalf("case created tasks = %d, want 3", n)
	}
	// rescanning never duplicates (dedupe key on the task type)
	e.clk.Advance(10 * time.Minute)
	e.tick()
	if n := e.count("SELECT COUNT(1) FROM bw_task"); n != 7 {
		t.Fatalf("after rescan tasks = %d, want 7", n)
	}
}

// 需求二 · first seen at ≥ 7 days: go straight to the 7d node, no stale 48h card.
func TestOngoingReviewFirstSeenAfter7Days(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261006-0417")
	w := e.active(tk)
	if w.NodeKey != "rm_7d" {
		t.Fatalf("node = %s, want rm_7d", w.NodeKey)
	}
	if e.count("SELECT COUNT(1) FROM bw_work_item WHERE task_id=? AND node_key='rm_48h'", tk.ID) != 0 {
		t.Fatal("must not send an expired 48h card")
	}
	if _, err := e.click(tk, "rm_a", "close", "k1"); err != nil {
		t.Fatal(err)
	}
	tk = e.reload(tk)
	if tk.Status != task.StatusClosed || tk.CloseReason != task.CloseCompleted {
		t.Fatalf("status=%s reason=%s", tk.Status, tk.CloseReason)
	}
}

// 需求二 · 48h Closed → wait → 7 days still in Maker Review → refresh the same card.
func TestOngoingReview48hClosedThen7d(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261011-0391") // entered 50h ago
	if w := e.active(tk); w.NodeKey != "rm_48h" || tk.Assignee != "rm_a" {
		t.Fatalf("node=%s assignee=%s", w.NodeKey, tk.Assignee)
	}
	firstMsg := e.reload(tk).LarkMessageID
	if firstMsg == "" {
		t.Fatal("48h card not delivered to Lark")
	}
	if _, err := e.click(tk, "rm_a", "close", "c-48h"); err != nil {
		t.Fatal(err)
	}
	tk = e.reload(tk)
	if tk.Status != task.StatusOpen || tk.NextActionAt == 0 {
		t.Fatalf("after 48h Closed: status=%s next=%d", tk.Status, tk.NextActionAt)
	}
	// 7d - 50h = 118h later the wait fires
	e.advance(117 * time.Hour)
	if e.reload(tk).Status != task.StatusOpen {
		t.Fatal("must still be waiting before 7 days")
	}
	e.advance(2 * time.Hour)
	tk = e.reload(tk)
	w := e.active(tk)
	if w.NodeKey != "rm_7d" || tk.Status != task.StatusProcessing {
		t.Fatalf("node=%s status=%s", w.NodeKey, tk.Status)
	}
	if tk.LarkMessageID != firstMsg {
		t.Fatalf("7d must refresh the same Lark card: %s vs %s", tk.LarkMessageID, firstMsg)
	}
	if e.count("SELECT COUNT(1) FROM bw_task WHERE object_key='AP-20261011-0391'") != 1 {
		t.Fatal("one Maker Review round must be one task")
	}
	// escalate via external jump completes the round and ends the task
	out, err := e.svc.OpenLink(e.ctx, viewer("rm_a"), tk.ID, task.OpenLinkReq{WorkItemID: w.ID, Button: "escalate", RequestKey: "esc-1", CardRev: w.CardRev})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.URL, "https://lark.example.com/") || !strings.Contains(out.URL, "bw_sig=") || !out.Completed {
		t.Fatalf("bad open-link %+v", out)
	}
	if tk = e.reload(tk); tk.CloseReason != task.CloseCompleted {
		t.Fatalf("reason=%s", tk.CloseReason)
	}
}

// 需求二 · nobody clicks the 48h card: node deadline voids the round at 7d.
func TestOngoingReviewDeadlineVoidsRound(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261009-0204") // entered 72h ago, rm_b
	first := e.active(tk)
	e.advance(96*time.Hour + time.Minute)
	var old task.WorkItem
	_ = e.conn.QueryRowCtx(e.ctx, &old, "SELECT * FROM bw_work_item WHERE id=?", first.ID)
	if old.Status != "voided" {
		t.Fatalf("48h round status = %s, want voided", old.Status)
	}
	if w := e.active(tk); w.NodeKey != "rm_7d" {
		t.Fatalf("node = %s", w.NodeKey)
	}
}

// 需求二 · case leaves Maker Review before 7d → ends as Case 已失效.
func TestOngoingReviewLeftStage(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261009-0204")
	if _, err := e.click(tk, "rm_b", "close", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.conn.ExecCtx(e.ctx, "UPDATE demo_case SET stage='checker_review' WHERE appeal_id='AP-20261009-0204'"); err != nil {
		t.Fatal(err)
	}
	e.advance(97 * time.Hour)
	tk = e.reload(tk)
	if tk.Status != task.StatusClosed || tk.CloseReason != task.CloseInvalidated {
		t.Fatalf("status=%s reason=%s", tk.Status, tk.CloseReason)
	}
}

// 需求一 · 延迟处理: remind at case.created_at + 3d, re-check, second round only Closed.
func TestCaseCreatedDelayRemind(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_CASE_CREATED", "AP-20261013-0532") // created 2h ago, rm_b
	if _, err := e.click(tk, "rm_b", "delay", "d1"); err != nil {
		t.Fatal(err)
	}
	tk = e.reload(tk)
	created := time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)
	if tk.Status != task.StatusOpen || tk.NextActionAt != created.Add(72*time.Hour).UnixMilli() {
		t.Fatalf("status=%s next=%v", tk.Status, clock.FromMs(tk.NextActionAt))
	}
	e.advance(69 * time.Hour)
	if e.reload(tk).Status != task.StatusOpen {
		t.Fatal("reminded too early")
	}
	e.advance(time.Hour + time.Minute)
	tk = e.reload(tk)
	w := e.active(tk)
	if w.Round != 2 || tk.Status != task.StatusProcessing {
		t.Fatalf("round=%d status=%s", w.Round, tk.Status)
	}
	var btns []task.HumanButton
	_ = json.Unmarshal([]byte(w.ButtonsJSON), &btns)
	if len(btns) != 1 || btns[0].Key != "close" {
		t.Fatalf("second round buttons = %+v, want only Closed", btns)
	}
	var card task.RenderedCard
	_ = json.Unmarshal([]byte(w.CardJSON), &card)
	if card.Title != "VIP Case 复查提醒" || card.RoundLabel != "T+3" {
		t.Fatalf("card = %q / %q", card.Title, card.RoundLabel)
	}
}

// 需求一 · clicking delay after created+3d already passed re-checks immediately.
func TestCaseCreatedDelayAlreadyPassed(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_CASE_CREATED", "AP-20261010-0288") // created 80h ago
	if _, err := e.click(tk, "rm_a", "delay", "d1"); err != nil {
		t.Fatal(err)
	}
	if w := e.active(e.reload(tk)); w.Round != 2 {
		t.Fatalf("round = %d, want immediate second round", w.Round)
	}
}

// 需求一 · delayed, then the case closes → the reminder ends the task as invalid.
func TestCaseCreatedDelayedCaseClosed(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_CASE_CREATED", "AP-20261013-0532")
	if _, err := e.click(tk, "rm_b", "delay", "d1"); err != nil {
		t.Fatal(err)
	}
	_, _ = e.conn.ExecCtx(e.ctx, "UPDATE demo_case SET status='closed' WHERE appeal_id='AP-20261013-0532'")
	e.advance(71 * time.Hour)
	tk = e.reload(tk)
	if tk.CloseReason != task.CloseInvalidated {
		t.Fatalf("reason=%s", tk.CloseReason)
	}
}

// 运行规则 1 · close event closes the task and updates the card.
func TestObjectClosedEvent(t *testing.T) {
	e := newEnv(t)
	e.tick()
	n, err := e.svc.ObjectEvent(e.ctx, "case.closed", "AP-20261009-0204", "evt-1")
	if err != nil || n != 1 {
		t.Fatalf("closed=%d err=%v", n, err)
	}
	if again, _ := e.svc.ObjectEvent(e.ctx, "case.closed", "AP-20261009-0204", "evt-1"); again != 0 {
		t.Fatal("duplicate event must be ignored")
	}
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261009-0204")
	if tk.CloseReason != task.CloseInvalidated || e.count("SELECT COUNT(1) FROM bw_work_item WHERE task_id=? AND status='active'", tk.ID) != 0 {
		t.Fatal("task must close and void its work item")
	}
}

func TestActionIdempotencyAndStaleCard(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261011-0391")
	w := e.active(tk)
	req := task.ActionReq{WorkItemID: w.ID, Button: "close", RequestKey: "same", CardRev: w.CardRev}
	if _, err := e.svc.Act(e.ctx, viewer("rm_a"), tk.ID, req); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Act(e.ctx, viewer("rm_a"), tk.ID, req)
	if err != nil || !res.Duplicate {
		t.Fatalf("replay: res=%+v err=%v", res, err)
	}
	req2 := req
	req2.Note = "different"
	if _, err := e.svc.Act(e.ctx, viewer("rm_a"), tk.ID, req2); err == nil {
		t.Fatal("same key with different params must be rejected")
	}
	// another RM cannot act
	tk2 := e.task("VIP_ONGOING_REVIEW", "AP-20261009-0204")
	if _, err := e.click(tk2, "rm_a", "close", "zz"); err == nil {
		t.Fatal("non-assignee must be rejected")
	}
	// stale card revision
	w2 := e.active(tk2)
	_, err = e.svc.Act(e.ctx, viewer("rm_b"), tk2.ID, task.ActionReq{WorkItemID: w2.ID, Button: "close", RequestKey: "old", CardRev: w2.CardRev - 1})
	var te *task.Error
	if err == nil || !asErr(err, &te) || te.Code != 409 {
		t.Fatalf("stale card must 409, got %v", err)
	}
}

func asErr(err error, te **task.Error) bool {
	x, ok := err.(*task.Error)
	*te = x
	return ok
}

// RM mapping missing → leader attention; leader assigns; Lark failure flagged.
func TestLeaderOps(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261013-0551")
	if !strings.Contains(tk.Attention, task.AttnRMMissing) || tk.Status != task.StatusOpen {
		t.Fatalf("attention=%q status=%s", tk.Attention, tk.Status)
	}
	leader := task.Viewer{UserID: "leader_l", Role: "leader", Team: "VIP_RM"}
	q := must(e.svc.LeaderQueue(e.ctx, leader, ""))
	if q.Stats["attention"] < 1 {
		t.Fatalf("attention stat = %d", q.Stats["attention"])
	}
	if err := e.svc.Reassign(e.ctx, leader, tk.ID, "rm_b", "临时接手"); err != nil {
		t.Fatal(err)
	}
	tk = e.reload(tk)
	if tk.Assignee != "rm_b" || tk.Status != task.StatusProcessing || tk.Attention != "" {
		t.Fatalf("after reassign: %+v", tk)
	}
	// rm_c's Lark open id always fails → after max tries flagged 投递失败
	tc := e.task("VIP_CASE_CREATED", "AP-20261012-0640")
	for i := 0; i < 5; i++ {
		e.advance(time.Hour)
	}
	if !strings.Contains(e.reload(tc).Attention, task.AttnDeliveryFailed) {
		t.Fatalf("attention=%q", e.reload(tc).Attention)
	}
	// RM leaves the team → their active items surface to the leader
	if err := e.svc.SetMemberActive(e.ctx, leader, "rm_a", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.task("VIP_ONGOING_REVIEW", "AP-20261006-0417").Attention, task.AttnAssigneeLeft) {
		t.Fatal("assignee_left flag missing")
	}
	// cancel
	if err := e.svc.Cancel(e.ctx, leader, tc.ID, "重复"); err != nil {
		t.Fatal(err)
	}
	if e.reload(tc).CloseReason != task.CloseAdmin {
		t.Fatal("cancel must close as admin_terminated")
	}
}

func TestDetailExplains(t *testing.T) {
	e := newEnv(t)
	e.tick()
	tk := e.task("VIP_ONGOING_REVIEW", "AP-20261011-0391")
	d := must(e.svc.Detail(e.ctx, viewer("rm_a"), tk.ID))
	if len(d.Explain.Why) == 0 || len(d.Explain.WhyMe) == 0 || len(d.Explain.Next) == 0 || !d.CanAct {
		t.Fatalf("explain = %+v canAct=%v", d.Explain, d.CanAct)
	}
	if !strings.Contains(strings.Join(d.Explain.WhyMe, " "), "VIP_UID_TO_RM") {
		t.Fatalf("why me = %v", d.Explain.WhyMe)
	}
	if _, err := e.svc.Detail(e.ctx, viewer("rm_b"), tk.ID); err == nil {
		t.Fatal("other RM must not see the task")
	}
	// sensitive profit is masked for a config viewer, visible to RM
	cfgView := must(e.svc.Detail(e.ctx, task.Viewer{UserID: "cfg_c", Role: "config", Team: "VIP_RM"}, tk.ID))
	for _, f := range cfgView.Fields {
		if f.Field == "vip.six_month_profit" && f.Value != "••••" {
			t.Fatalf("profit not masked for config role: %s", f.Value)
		}
	}
}

func TestPublishContractAndBreaker(t *testing.T) {
	e := newEnv(t)
	key := "VIP_ONGOING_REVIEW"
	// a flow draft that needs a field the active config does not produce
	d := must(e.svc.FlowDraft(e.ctx, "cfg_c", key))
	var cards map[string]task.CardTemplate
	_ = json.Unmarshal(d.Cards, &cards)
	c := cards["vip_review_card"]
	c.Fields = append(c.Fields, task.CardField{Field: "vip.tier_note", Label: "备注"})
	cards["vip_review_card"] = c
	must(e.svc.SaveFlowDraft(e.ctx, "cfg_c", key, task.FlowDraftReq{Graph: d.Graph, Layout: d.Layout, Cards: mustJSON(cards)}))
	p := must(e.svc.PlanPublish(e.ctx, "cfg_c", key, "flow"))
	if p.OK {
		t.Fatal("flow-only publish must fail the field contract")
	}
	for _, o := range p.Options {
		if o.Scope == "flow" && o.Available {
			t.Fatal("flow scope must be unavailable")
		}
	}
	// add the field to the config draft → publish both together
	cd := must(e.svc.ConfigDraft(e.ctx, "cfg_c", key))
	cd.Spec.Fields = append(cd.Spec.Fields, task.FieldMap{Column: "vip_source", Field: "vip.tier_note", Type: "string"})
	must(e.svc.SaveConfigDraft(e.ctx, "cfg_c", key, *cd.Spec, ""))
	p = must(e.svc.Publish(e.ctx, "cfg_c", key, task.PublishReq{Scope: "both", Rescan: "continue"}))
	if p.ConfigTo != 2 || p.FlowTo != 2 {
		t.Fatalf("versions %d/%d", p.ConfigTo, p.FlowTo)
	}
	// circuit breaker: cap 1 while 4 cases are eligible
	cd = must(e.svc.ConfigDraft(e.ctx, "cfg_c", key))
	cd.Spec.MaxPerRun = 1
	must(e.svc.SaveConfigDraft(e.ctx, "cfg_c", key, *cd.Spec, ""))
	must(e.svc.Publish(e.ctx, "cfg_c", key, task.PublishReq{Scope: "config", Rescan: "continue"}))
	e.clk.Advance(6 * time.Minute)
	e.tick()
	var sc task.ScheduleRow
	_ = e.conn.QueryRowCtx(e.ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", key)
	if sc.Status != "tripped" || e.count("SELECT COUNT(1) FROM bw_task WHERE type_key=?", key) != 0 {
		t.Fatalf("schedule=%s tasks=%d", sc.Status, e.count("SELECT COUNT(1) FROM bw_task WHERE type_key=?", key))
	}
}

func TestShadowRun(t *testing.T) {
	e := newEnv(t)
	key := "VIP_ONGOING_REVIEW"
	must(e.svc.ChangeStatus(e.ctx, "cfg_c", key, task.StatusReq{Action: "disable"}))
	must(e.svc.ChangeStatus(e.ctx, "cfg_c", key, task.StatusReq{Action: "start_shadow"}))
	e.tick()
	shadow := e.count("SELECT COUNT(1) FROM bw_task WHERE type_key=? AND is_shadow=1", key)
	if shadow != 4 {
		t.Fatalf("shadow tasks = %d", shadow)
	}
	if e.count("SELECT COUNT(1) FROM outbox o JOIN bw_task t ON t.id=o.task_id WHERE t.is_shadow=1") != 0 {
		t.Fatal("shadow tasks must never be pushed")
	}
	rows, _, _ := e.svc.MyTasks(e.ctx, viewer("rm_a"), "pending")
	for _, r := range rows {
		if r.IsShadow {
			t.Fatal("shadow task in someone's todo")
		}
	}
	must(e.svc.ChangeStatus(e.ctx, "cfg_c", key, task.StatusReq{Action: "enable"}))
	if n := e.count("SELECT COUNT(1) FROM bw_task WHERE type_key=? AND is_shadow=1 AND close_reason='shadow_ended'", key); n != 4 {
		t.Fatalf("shadow tasks closed = %d", n)
	}
	e.clk.Advance(6 * time.Minute)
	e.tick()
	if n := e.count("SELECT COUNT(1) FROM bw_task WHERE type_key=? AND is_shadow=0", key); n != 4 {
		t.Fatalf("real tasks after enable = %d (shadow dedupe keys must not block)", n)
	}
}

func TestPushIdempotent(t *testing.T) {
	e := newEnv(t)
	payload := map[string]any{"appeal_id": "AP-NEW-1", "uid": "8812003390", "case_type": "withdrawal_appeal", "status": "open",
		"created_at": e.clk.Now().UnixMilli(), "is_vip": 1, "vip_level": "VIP 3"}
	r1 := must(e.svc.Push(e.ctx, "VIP_CASE_CREATED", "evt-9", payload))
	r2 := must(e.svc.Push(e.ctx, "VIP_CASE_CREATED", "evt-9", payload))
	if !r1.Created || !r2.Duplicate {
		t.Fatalf("r1=%+v r2=%+v", r1, r2)
	}
	r3 := must(e.svc.Push(e.ctx, "VIP_CASE_CREATED", "evt-10", payload))
	if r3.Created || r3.Reason == "" {
		t.Fatalf("second event for the same case must dedupe: %+v", r3)
	}
	payload["appeal_id"], payload["is_vip"] = "AP-NEW-2", 0
	if r := must(e.svc.Push(e.ctx, "VIP_CASE_CREATED", "evt-11", payload)); r.Created {
		t.Fatal("ineligible object must not create a task")
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
