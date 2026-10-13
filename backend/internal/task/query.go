package task

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bywork/internal/clock"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// ---------- my tasks ----------

type TaskRow struct {
	ID           int64    `json:"id"`
	TypeKey      string   `json:"type_key"`
	TypeName     string   `json:"type_name"`
	Title        string   `json:"title"`
	Badge        string   `json:"badge"`
	Summary      []RField `json:"summary"`
	Status       string   `json:"status"`
	CloseReason  string   `json:"close_reason"`
	Round        int64    `json:"round"`
	Assignee     string   `json:"assignee"`
	AssigneeName string   `json:"assignee_name"`
	Attention    []string `json:"attention"`
	NextActionAt int64    `json:"next_action_at"`
	NextText     string   `json:"next_text"`
	Result       string   `json:"result"`
	UpdatedAt    int64    `json:"updated_at"`
	CreatedAt    int64    `json:"created_at"`
	PlacedAt     int64    `json:"placed_at"` // active work item created_at
	PlacedMs     int64    `json:"placed_ms"` // how long it has been waiting on a person
	UID          string   `json:"uid,omitempty"`
	IsShadow     bool     `json:"is_shadow"`
	ObjectKey    string   `json:"object_key"`
}

func (s *Service) row(ctx context.Context, v Viewer, t *Task) TaskRow {
	r := TaskRow{ID: t.ID, TypeKey: t.TypeKey, TypeName: t.Title, Title: t.Title, Status: t.Status, CloseReason: t.CloseReason,
		Assignee: t.Assignee, AssigneeName: s.memberName(ctx, s.conn, t.Assignee), Attention: t.AttentionFlags(), NextActionAt: t.NextActionAt,
		NextText: t.NextActionText, UpdatedAt: t.UpdatedAt, CreatedAt: t.CreatedAt, IsShadow: t.IsShadow == 1, ObjectKey: t.ObjectKey, Summary: []RField{}}
	if cv, err := s.cardView(ctx, s.conn, t); err == nil {
		c := MaskCard(cv.Card, v.CanSeeSensitive())
		if c.Title != "" {
			r.Title = c.Title
		}
		r.Badge, r.Round = c.RoundLabel, c.Round
		if r.Round <= 1 && r.Badge == "" {
			r.Badge = "新建"
		}
		for _, f := range c.Fields {
			if f.List && len(r.Summary) < 3 {
				r.Summary = append(r.Summary, f)
			}
		}
		r.Result = cv.Result
	}
	if wi, err := s.activeWorkItem(ctx, s.conn, t.ID); err == nil {
		r.PlacedAt, r.PlacedMs = wi.CreatedAt, s.nowMs()-wi.CreatedAt
	}
	if v.IsLeaderOf(t.Team) {
		r.UID = t.Snapshot().String("case.uid") // needed to set a temporary RM override
	}
	return r
}

// MyTasks groups: pending (待处理), waiting (等待提醒), done (已结束).
func (s *Service) MyTasks(ctx context.Context, v Viewer, group string) ([]TaskRow, map[string]int, error) {
	counts := map[string]int{}
	queries := map[string]string{
		"pending": "SELECT * FROM bw_task WHERE is_shadow=0 AND status='PROCESSING' AND id IN (SELECT task_id FROM bw_work_item WHERE assignee=? AND status='active') ORDER BY updated_at DESC LIMIT 200",
		"waiting": "SELECT * FROM bw_task WHERE is_shadow=0 AND status='OPEN' AND assignee=? AND next_action_at>0 ORDER BY next_action_at LIMIT 200",
		"done":    "SELECT * FROM bw_task WHERE is_shadow=0 AND status='CLOSED' AND id IN (SELECT task_id FROM bw_work_item WHERE assignee=?) ORDER BY closed_at DESC LIMIT 50",
	}
	out := []TaskRow{}
	for g, q := range queries {
		var ts []Task
		if err := s.conn.QueryRowsCtx(ctx, &ts, q, v.UserID); err != nil {
			return nil, nil, err
		}
		counts[g] = len(ts)
		if g == group {
			for i := range ts {
				out = append(out, s.row(ctx, v, &ts[i]))
			}
		}
	}
	return out, counts, nil
}

// ---------- detail ----------

type Explain struct {
	Why   []string `json:"why"`
	WhyMe []string `json:"why_me"`
	Next  []string `json:"next"`
}

type TimelineItem struct {
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Actor string `json:"actor"`
	Text  string `json:"text"`
}

type Detail struct {
	Task         *Task             `json:"task"`
	TypeName     string            `json:"type_name"`
	TypeStatus   string            `json:"type_status"`
	View         *CardView         `json:"view"`
	Fields       []RField          `json:"fields"`
	SourceLine   string            `json:"source_line"`
	Explain      Explain           `json:"explain"`
	Timeline     []TimelineItem    `json:"timeline"`
	CanAct       bool              `json:"can_act"`
	CanManage    bool              `json:"can_manage"`
	AssigneeName string            `json:"assignee_name"`
	Attention    []string          `json:"attention"`
	WorkItems    []WorkItem        `json:"work_items"`
	Executions   []engine.NodeExec `json:"executions,omitempty"`
	Snapshot     expr.Vars         `json:"snapshot,omitempty"`
	AISummary    string            `json:"ai_summary"`
}

func (s *Service) canView(v Viewer, t *Task, items []WorkItem) bool {
	if v.Role == "admin" || v.Role == "config" || v.IsLeaderOf(t.Team) {
		return true
	}
	for _, w := range items {
		if w.Assignee == v.UserID {
			return true
		}
	}
	return false
}

func (s *Service) Detail(ctx context.Context, v Viewer, id int64) (*Detail, error) {
	t, err := s.Task(ctx, s.conn, id)
	if err != nil {
		return nil, err
	}
	items, err := s.workItems(ctx, s.conn, id)
	if err != nil {
		return nil, err
	}
	if !s.canView(v, t, items) {
		return nil, Forbidden("无权查看该任务")
	}
	tt, err := s.Type(ctx, s.conn, t.TypeKey)
	if err != nil {
		return nil, err
	}
	view, err := s.cardView(ctx, s.conn, t)
	if err != nil {
		return nil, err
	}
	view.Card = MaskCard(view.Card, v.CanSeeSensitive())
	d := &Detail{Task: t, TypeName: tt.Name, TypeStatus: tt.Status, View: view, Fields: view.Card.Fields, WorkItems: items,
		AssigneeName: s.memberName(ctx, s.conn, t.Assignee), Attention: t.AttentionFlags(), CanManage: v.IsLeaderOf(t.Team), Timeline: []TimelineItem{}}
	if view.State == "actionable" {
		if wi, err := s.activeWorkItem(ctx, s.conn, id); err == nil && wi.Assignee == v.UserID {
			d.CanAct = true
		}
	}
	// source line: where facts come from and when they were read
	var readAt string
	if run, err := s.loadRun(ctx, t.RunID); err == nil {
		vars := run.Vars()
		for k, val := range vars {
			if strings.HasSuffix(k, ".read_at") {
				if tm, ok := expr.ToTime(val); ok {
					readAt = s.fmtTime(tm.UnixMilli())
				}
			}
		}
		if v.Role == "admin" || v.Role == "config" {
			d.Snapshot = vars
		}
	}
	d.SourceLine = fmt.Sprintf("快照于 %s 创建（配置 v%d）", s.fmtTime(t.CreatedAt), t.ConfigVersion)
	if readAt != "" {
		d.SourceLine += " · 实时字段 " + readAt + " 读取"
	}
	if se := t.Snapshot().String("case.stage_entered_at"); se != "" {
		if tm, ok := expr.ToTime(se); ok {
			d.SourceLine += " · 进入 Maker Review 于 " + s.fmtTime(tm.UnixMilli())
		}
	}
	d.Explain = s.explain(ctx, t, tt, items)
	logs, _ := s.Logs(ctx, id)
	for _, l := range logs {
		actor := l.Actor
		if actor != "" && actor != "system" {
			actor = s.memberName(ctx, s.conn, actor)
		}
		d.Timeline = append(d.Timeline, TimelineItem{At: l.At, Kind: l.Kind, Actor: actor, Text: l.Text})
	}
	if v.Role == "admin" || v.Role == "config" {
		d.Executions, _ = s.eng.Executions(ctx, t.RunID)
	}
	return d, nil
}

func (s *Service) loadRun(ctx context.Context, id int64) (*engine.Run, error) {
	var r engine.Run
	err := s.conn.QueryRowCtx(ctx, &r, "SELECT * FROM wf_run WHERE id=?", id)
	return &r, err
}

func (s *Service) Logs(ctx context.Context, taskID int64) ([]LogEntry, error) {
	out := []LogEntry{}
	err := s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_task_log WHERE task_id=? ORDER BY id", taskID)
	return out, err
}

// explain answers: 为什么有这个任务 / 为什么是我 / 接下来会发生什么.
func (s *Service) explain(ctx context.Context, t *Task, tt *TaskType, items []WorkItem) Explain {
	e := Explain{Why: []string{}, WhyMe: []string{}, Next: []string{}}
	o := t.Origin()
	src := map[string]string{"scan": "扫描命中", "push": "业务 Push 事件", "manual": "管理员补建", "backfill": "回扫命中"}[o.Source]
	e.Why = append(e.Why, fmt.Sprintf("规则「%s」v%d · %s %s", tt.Name, o.ConfigVersion, s.fmtTime(o.At), src))
	if o.EventID != "" {
		e.Why = append(e.Why, "事件 "+o.EventID)
	}
	if len(o.Matched) > 0 {
		e.Why = append(e.Why, "命中："+strings.Join(o.Matched, "、"))
	}
	if t.IsShadow == 1 {
		e.Why = append(e.Why, "影子运行任务：不推送、不进入任何人的待办")
	}

	if len(items) > 0 {
		w := items[len(items)-1]
		r := w.Reason()
		switch r.Via {
		case "mapping":
			e.WhyMe = append(e.WhyMe, fmt.Sprintf("按规则 %s 解析到 %s · 无临时例外", r.Rule, s.memberName(ctx, s.conn, w.Assignee)))
		case "override":
			e.WhyMe = append(e.WhyMe, fmt.Sprintf("临时 RM 例外（审批 %s）指向 %s，优先于权威映射", r.Approval, s.memberName(ctx, s.conn, w.Assignee)))
		case "leader":
			e.WhyMe = append(e.WhyMe, fmt.Sprintf("由 %s 改派%s", s.memberName(ctx, s.conn, r.By), notePart(r.Note)))
		case "fixed", "field":
			e.WhyMe = append(e.WhyMe, fmt.Sprintf("按规则 %s 分配", r.Rule))
		case "missing":
			e.WhyMe = append(e.WhyMe, "RM 映射缺失，等待 Leader 指派"+notePart(r.Note))
		}
		if r.Team != "" {
			e.WhyMe = append(e.WhyMe, "团队 "+r.Team)
		}
	}

	if t.Status == StatusClosed {
		e.Next = append(e.Next, "任务已结束 · "+CloseReasonText[t.CloseReason])
		return e
	}
	timers, _ := s.eng.PendingTimers(ctx, s.conn, t.RunID)
	def, _ := s.eng.Definition(ctx, s.conn, t.TypeKey, t.FlowVersion)
	for _, tm := range timers {
		key := tm.TimerKey[strings.Index(tm.TimerKey, ":")+1:]
		at := s.fmtTime(tm.DueAt)
		nodeName := tm.NodeKey
		if def != nil {
			if n := def.Graph.Node(tm.NodeKey); n != nil {
				nodeName = n.Name
			}
		}
		switch {
		case key == "deadline":
			e.Next = append(e.Next, fmt.Sprintf("%s 「%s」节点定时到期：如仍未处理，本轮待办作废并进入下一步复查", at, nodeName))
		case strings.HasPrefix(key, "remind"):
			e.Next = append(e.Next, fmt.Sprintf("延迟后，%s 会复查 Case 并再次提醒", at))
		case key == "wait":
			e.Next = append(e.Next, fmt.Sprintf("%s %s，然后实时复查", at, nodeName))
		case strings.HasPrefix(key, "retry"):
			e.Next = append(e.Next, fmt.Sprintf("%s 自动重试读取", at))
		}
	}
	if t.Status == StatusProcessing {
		e.Next = append(e.Next, "点击按钮完成本轮；不处理会一直留在待办，不会自动升级或改派")
	}
	if strings.Contains(t.Attention, AttnRMMissing) {
		e.Next = append(e.Next, "等待 Leader 指派或设置临时 RM 例外")
	}
	return e
}

// ---------- leader queue ----------

type AttentionRow struct {
	TaskRow
	Reasons  []string `json:"reasons"`
	Duration int64    `json:"duration_ms"`
}

type MemberLoad struct {
	Member
	Pending      int   `json:"pending"`
	Waiting      int   `json:"waiting"`
	OldestMs     int64 `json:"oldest_ms"`
	MedianDoneMs int64 `json:"median_done_ms"`
}

type LeaderQueue struct {
	Team      string         `json:"team"`
	Stats     map[string]int `json:"stats"`
	Attention []AttentionRow `json:"attention"`
	Pending   []TaskRow      `json:"pending"`
	Members   []MemberLoad   `json:"members"`
	Overrides []Override     `json:"overrides"`
}

func (s *Service) LeaderQueue(ctx context.Context, v Viewer, team string) (*LeaderQueue, error) {
	if team == "" {
		team = v.Team
	}
	if !v.IsLeaderOf(team) {
		return nil, Forbidden("只有团队 Leader 可以查看团队队列")
	}
	now := s.nowMs()
	q := &LeaderQueue{Team: team, Stats: map[string]int{}, Attention: []AttentionRow{}, Pending: []TaskRow{}, Members: []MemberLoad{}}
	var open []Task
	if err := s.conn.QueryRowsCtx(ctx, &open, "SELECT * FROM bw_task WHERE team=? AND is_shadow=0 AND status<>'CLOSED' ORDER BY id", team); err != nil {
		return nil, err
	}
	for i := range open {
		t := &open[i]
		flags := t.AttentionFlags()
		if len(flags) > 0 {
			reasons := []string{}
			for _, f := range flags {
				reasons = append(reasons, AttentionText[f])
			}
			q.Attention = append(q.Attention, AttentionRow{TaskRow: s.row(ctx, v, t), Reasons: reasons, Duration: now - t.UpdatedAt})
			continue
		}
		if t.Status == StatusProcessing {
			q.Pending = append(q.Pending, s.row(ctx, v, t))
		}
	}
	sort.Slice(q.Pending, func(i, j int) bool { return q.Pending[i].PlacedAt < q.Pending[j].PlacedAt })
	q.Stats["attention"] = len(q.Attention)
	for _, t := range open {
		if t.Assignee == "" {
			q.Stats["unassigned"]++
		} else if len(t.AttentionFlags()) == 0 {
			q.Stats["processing"]++
		}
	}
	var closedToday int64
	_ = s.conn.QueryRowCtx(ctx, &closedToday, "SELECT COUNT(1) FROM bw_task WHERE team=? AND is_shadow=0 AND status='CLOSED' AND closed_at >= ?", team, now-24*3600*1000)
	q.Stats["closed_today"] = int(closedToday)

	members, err := s.Members(ctx, team)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.Role != "rm" {
			continue
		}
		ml := MemberLoad{Member: m}
		for _, t := range open {
			if t.Assignee != m.UserID {
				continue
			}
			if t.Status == StatusProcessing {
				ml.Pending++
				if wi, err := s.activeWorkItem(ctx, s.conn, t.ID); err == nil && (ml.OldestMs == 0 || now-wi.CreatedAt > ml.OldestMs) {
					ml.OldestMs = now - wi.CreatedAt
				}
			} else if t.NextActionAt > 0 {
				ml.Waiting++
			}
		}
		var durs []int64
		_ = s.conn.QueryRowsCtx(ctx, &durs, "SELECT done_at - created_at FROM bw_work_item WHERE assignee=? AND status IN ('done','delayed') AND done_at > 0 ORDER BY id DESC LIMIT 200", m.UserID)
		ml.MedianDoneMs = percentile(durs, 50)
		q.Members = append(q.Members, ml)
	}
	q.Overrides, _ = s.Overrides(ctx)
	return q, nil
}

func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int64{}, xs...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	idx := (len(c) - 1) * p / 100
	return c[idx]
}

// ---------- task type views ----------

func (s *Service) TypeTasks(ctx context.Context, v Viewer, key, status string, limit int) ([]TaskRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var ts []Task
	var err error
	if status == "" {
		err = s.conn.QueryRowsCtx(ctx, &ts, "SELECT * FROM bw_task WHERE type_key=? ORDER BY id DESC LIMIT ?", key, limit)
	} else {
		err = s.conn.QueryRowsCtx(ctx, &ts, "SELECT * FROM bw_task WHERE type_key=? AND status=? ORDER BY id DESC LIMIT ?", key, status, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]TaskRow, 0, len(ts))
	for i := range ts {
		out = append(out, s.row(ctx, v, &ts[i]))
	}
	return out, nil
}

func (s *Service) ScheduleInfo(ctx context.Context, key string) (map[string]any, error) {
	var sc ScheduleRow
	if err := s.conn.QueryRowCtx(ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", key); err != nil {
		return nil, err
	}
	var runs []ScheduleRun
	_ = s.conn.QueryRowsCtx(ctx, &runs, "SELECT * FROM bw_schedule_run WHERE type_key=? ORDER BY id DESC LIMIT 20", key)
	var next []int64
	for _, t := range NextFires(sc.Cron, sc.Timezone, s.now(), 3) {
		next = append(next, clock.Ms(t))
	}
	lag := int64(0)
	if sc.Status == "active" && sc.NextFireAt > 0 && s.nowMs() > sc.NextFireAt {
		lag = s.nowMs() - sc.NextFireAt
	}
	return map[string]any{"schedule": sc, "runs": runs, "next_fires": next, "lag_ms": lag}, nil
}

type Metrics struct {
	Total          int            `json:"total"`
	Closed         int            `json:"closed"`
	FalsePositive  float64        `json:"false_positive_rate"`
	FeedbackCounts map[string]int `json:"feedback"`
	P50Ms          int64          `json:"p50_ms"`
	P90Ms          int64          `json:"p90_ms"`
	DelayDist      map[string]int `json:"delay_distribution"`
	IdleDist       map[string]int `json:"idle_distribution"`
	CloseReasons   map[string]int `json:"close_reasons"`
}

// TypeMetrics: 误报率、处理时效 P50/P90、延迟次数分布、放置时长分布.
func (s *Service) TypeMetrics(ctx context.Context, key string) (*Metrics, error) {
	var ts []Task
	if err := s.conn.QueryRowsCtx(ctx, &ts, "SELECT * FROM bw_task WHERE type_key=? AND is_shadow=0", key); err != nil {
		return nil, err
	}
	m := &Metrics{Total: len(ts), FeedbackCounts: map[string]int{}, DelayDist: map[string]int{}, IdleDist: map[string]int{}, CloseReasons: map[string]int{}}
	var durs []int64
	now := s.nowMs()
	for _, t := range ts {
		m.DelayDist[fmt.Sprintf("%d", t.DelayCount)]++
		if t.Status == StatusClosed {
			m.Closed++
			m.CloseReasons[t.CloseReason]++
			if t.CloseReason == CloseCompleted {
				durs = append(durs, t.ClosedAt-t.CreatedAt)
			}
		} else if t.Status == StatusProcessing {
			if wi, err := s.activeWorkItem(ctx, s.conn, t.ID); err == nil {
				h := (now - wi.CreatedAt) / 3600000
				b := "<24h"
				switch {
				case h >= 72:
					b = "≥3d"
				case h >= 24:
					b = "1-3d"
				}
				m.IdleDist[b]++
			}
		}
	}
	m.P50Ms, m.P90Ms = percentile(durs, 50), percentile(durs, 90)
	type fb struct {
		Kind string `db:"kind"`
		N    int64  `db:"n"`
	}
	var fbs []fb
	_ = s.conn.QueryRowsCtx(ctx, &fbs, "SELECT kind, COUNT(DISTINCT task_id) AS n FROM bw_feedback WHERE type_key=? GROUP BY kind", key)
	for _, f := range fbs {
		m.FeedbackCounts[f.Kind] = int(f.N)
	}
	if m.Total > 0 {
		m.FalsePositive = float64(m.FeedbackCounts["should_not_exist"]+m.FeedbackCounts["not_mine"]) / float64(m.Total)
	}
	return m, nil
}

type AuditRow struct {
	ID     int64  `db:"id" json:"id"`
	At     int64  `db:"at" json:"at"`
	Actor  string `db:"actor" json:"actor"`
	Action string `db:"action" json:"action"`
	Target string `db:"target" json:"target"`
	Detail string `db:"detail" json:"detail"`
}

func (s *Service) AuditLog(ctx context.Context, target string, limit int) ([]AuditRow, error) {
	out := []AuditRow{}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var err error
	if target == "" {
		err = s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_audit ORDER BY id DESC LIMIT ?", limit)
	} else {
		err = s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_audit WHERE target=? ORDER BY id DESC LIMIT ?", target, limit)
	}
	return out, err
}
