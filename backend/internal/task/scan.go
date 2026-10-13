package task

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/expr"
)

// ValidateCron: 5 fields (no seconds), explicit timezone.
func ValidateCron(spec, tz string) (cron.Schedule, *time.Location, error) {
	if strings.TrimSpace(tz) == "" {
		return nil, nil, fmt.Errorf("必须明确时区")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("未知时区 %s", tz)
	}
	if n := len(strings.Fields(spec)); n != 5 {
		return nil, nil, fmt.Errorf("cron 必须为 5 段（不支持秒），当前 %d 段", n)
	}
	sch, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("cron 无效: %v", err)
	}
	return sch, loc, nil
}

func NextFires(spec, tz string, from time.Time, n int) []time.Time {
	sch, loc, err := ValidateCron(spec, tz)
	if err != nil {
		return nil
	}
	out := []time.Time{}
	t := from.In(loc)
	for i := 0; i < n; i++ {
		t = sch.Next(t)
		out = append(out, t.UTC())
	}
	return out
}

// ---------- trigger heartbeat ----------

// TriggerTick claims due scan plans (longest-waiting first), advances their
// next fire time on claim (missed fires collapse into one), and scans within
// a time budget. Unfinished scans checkpoint and continue on the next tick.
func (s *Service) TriggerTick(ctx context.Context) (int, error) {
	deadline := time.Now().Add(s.opt.ScanBudget)
	now := s.now()
	nowMs := clock.Ms(now)
	var due []ScheduleRow
	if err := s.conn.QueryRowsCtx(ctx, &due, "SELECT * FROM bw_schedule WHERE status='active' AND next_fire_at <= ? AND lease_until < ? ORDER BY next_fire_at LIMIT 20", nowMs, nowMs); err != nil {
		return 0, err
	}
	ran := 0
	for _, sc := range due {
		if time.Now().After(deadline) {
			break
		}
		next := nowMs + 60_000
		if f := NextFires(sc.Cron, sc.Timezone, now, 1); len(f) == 1 {
			next = clock.Ms(f[0])
		}
		n, err := db.Affected(ctx, s.conn, "UPDATE bw_schedule SET lease_owner=?, lease_until=?, next_fire_at=?, updated_at=? WHERE id=? AND lease_until < ? AND next_fire_at = ?",
			s.opt.Owner, nowMs+int64(s.opt.ScanBudget/time.Millisecond)+60_000, next, nowMs, sc.ID, nowMs, sc.NextFireAt)
		if err != nil || n == 0 {
			continue
		}
		res := s.scan(ctx, sc.TypeKey, "schedule", deadline)
		s.finishScan(ctx, &sc, res)
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_schedule SET lease_owner='', lease_until=0 WHERE id=?", sc.ID)
		ran++
	}
	if time.Now().Before(deadline) {
		s.recheckInvalidation(ctx, deadline)
	}
	return ran, nil
}

type ScanResult struct {
	Rows       int    `json:"rows"`
	Created    int    `json:"created"`
	Skipped    int    `json:"skipped"`
	Result     string `json:"result"` // success | failed | tripped | partial | skipped
	Message    string `json:"message"`
	DurationMs int64  `json:"duration_ms"`
	Checkpoint string `json:"-"`
}

// RunNow executes a scan immediately (配置页「立即执行一次」).
func (s *Service) RunNow(ctx context.Context, actor, typeKey string) (*ScanResult, error) {
	var sc ScheduleRow
	if err := s.conn.QueryRowCtx(ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", typeKey); err != nil {
		return nil, NotFound("扫描计划不存在")
	}
	res := s.scan(ctx, typeKey, "manual", time.Now().Add(s.opt.ScanBudget))
	s.finishScan(ctx, &sc, res)
	s.Audit(ctx, s.conn, actor, "run_now", typeKey, res)
	return &res, nil
}

func (s *Service) scan(ctx context.Context, typeKey, source string, deadline time.Time) (res ScanResult) {
	started := time.Now()
	defer func() { res.DurationMs = time.Since(started).Milliseconds() }()
	tt, err := s.Type(ctx, s.conn, typeKey)
	if err != nil {
		return ScanResult{Result: "failed", Message: err.Error()}
	}
	if tt.Status != TypeEnabled && tt.Status != TypeShadow {
		return ScanResult{Result: "skipped", Message: "任务类型未启用"}
	}
	cfg, err := s.Config(ctx, s.conn, typeKey, tt.ActiveConfigVersion)
	if err != nil {
		return ScanResult{Result: "failed", Message: err.Error()}
	}
	if !cfg.Spec.Trigger.Has("sql") {
		return ScanResult{Result: "skipped", Message: "该配置没有 SQL 触发来源"}
	}
	var sc ScheduleRow
	_ = s.conn.QueryRowCtx(ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", typeKey)
	vars := expr.Vars{"now": s.nowMs(), "scan_from": sc.Watermark, "last_scan_at": sc.LastSuccessAt}
	out, err := s.sources.Query(ctx, cfg.Spec.Trigger.Datasource, cfg.Spec.Trigger.Query, vars, 0)
	if err != nil {
		return ScanResult{Result: "failed", Message: err.Error()}
	}
	res.Rows = len(out.Rows)
	shadow := tt.Status == TypeShadow
	var todo []Candidate
	for _, row := range out.Rows {
		c := s.evaluate(ctx, tt, cfg.Spec, row, shadow)
		if c.Eligible && !c.Exists && c.DedupeKey != "" {
			todo = append(todo, c)
		} else {
			res.Skipped++
		}
	}
	// circuit breaker: a single run creating more than the cap pauses the type
	limit := cfg.Spec.MaxPerRun
	if limit > 0 && len(todo) > limit {
		msg := fmt.Sprintf("本次将新建 %d 个任务，超过单次上限 %d，已暂停扫描并告警", len(todo), limit)
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_schedule SET status='tripped', pause_reason=? WHERE type_key=?", msg, typeKey)
		s.Audit(ctx, s.conn, "system", "circuit_break", typeKey, map[string]any{"candidates": len(todo), "limit": limit})
		logx.WithContext(ctx).Errorf("ALERT bywork circuit breaker %s: %s", typeKey, msg)
		res.Result, res.Message = "tripped", msg
		return res
	}
	for i, c := range todo {
		if time.Now().After(deadline) {
			res.Result = "partial"
			res.Checkpoint = fmt.Sprintf("processed %d/%d", i, len(todo))
			res.Message = "时间预算用尽，已写入检查点，下一次心跳继续"
			return res
		}
		id, err := s.createTask(ctx, tt, cfg.Version, c, Origin{Source: "scan", At: s.nowMs(), ConfigVersion: cfg.Version, Matched: c.Matched})
		if err != nil {
			res.Result, res.Message = "failed", err.Error()
			return res
		}
		if id > 0 {
			res.Created++
		}
	}
	res.Result = "success"
	return res
}

func (s *Service) finishScan(ctx context.Context, sc *ScheduleRow, res ScanResult) {
	now := s.nowMs()
	_, _ = s.conn.ExecCtx(ctx, "INSERT INTO bw_schedule_run (type_key, config_version, source, fired_at, duration_ms, rows_scanned, tasks_created, result, message) VALUES (?,?,?,?,?,?,?,?,?)",
		sc.TypeKey, sc.ConfigVersion, "schedule", now, res.DurationMs, res.Rows, res.Created, res.Result, trunc(res.Message, 1000))
	switch res.Result {
	case "success", "skipped":
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_schedule SET last_run_at=?, last_success_at=?, consecutive_failures=0, checkpoint='' WHERE id=?", now, now, sc.ID)
	case "partial":
		// continue on the very next heartbeat
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_schedule SET last_run_at=?, checkpoint=?, next_fire_at=? WHERE id=?", now, res.Checkpoint, now, sc.ID)
	default:
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_schedule SET last_run_at=?, consecutive_failures=consecutive_failures+1 WHERE id=?", now, sc.ID)
	}
}

// ---------- invalidation ----------

// recheckInvalidation periodically re-validates open tasks of types that
// configure invalidation.recheck_every (fallback when no close event exists).
func (s *Service) recheckInvalidation(ctx context.Context, deadline time.Time) {
	var types []TaskType
	if err := s.conn.QueryRowsCtx(ctx, &types, "SELECT * FROM bw_task_type WHERE status IN ('enabled','shadow','disabled')"); err != nil {
		return
	}
	for _, tt := range types {
		cfg, err := s.Config(ctx, s.conn, tt.TypeKey, tt.ActiveConfigVersion)
		if err != nil || cfg.Spec.Invalidation.RecheckEvery == "" {
			continue
		}
		every, err := expr.ParseDuration(cfg.Spec.Invalidation.RecheckEvery)
		if err != nil {
			continue
		}
		var tasks []Task
		if err := s.conn.QueryRowsCtx(ctx, &tasks, "SELECT * FROM bw_task WHERE type_key=? AND status<>'CLOSED' AND last_checked_at < ? ORDER BY last_checked_at LIMIT 50",
			tt.TypeKey, s.nowMs()-every.Milliseconds()); err != nil {
			continue
		}
		for i := range tasks {
			if time.Now().After(deadline) {
				return
			}
			t := &tasks[i]
			ok, _, err := s.Revalidate(ctx, s.conn, t, t.Snapshot())
			if err != nil {
				logx.WithContext(ctx).Errorf("recheck task %d: %v", t.ID, err)
				continue
			}
			if !ok {
				_ = s.terminate(ctx, t, CloseInvalidated, "system", "定时复查：资格不再满足，自动关闭")
			}
		}
	}
}

// ObjectEvent handles business object events, e.g. case.closed: tasks whose
// config lists the event in invalidation.events close as "Case 已失效".
func (s *Service) ObjectEvent(ctx context.Context, event, objectKey, eventID string) (int, error) {
	if eventID != "" {
		if _, err := s.conn.ExecCtx(ctx, "INSERT INTO bw_push_event (event_id, type_key, received_at, result) VALUES (?,?,?,?)", eventID, event, s.nowMs(), "object_event"); db.IsDuplicate(err) {
			return 0, nil
		}
	}
	var tasks []Task
	if err := s.conn.QueryRowsCtx(ctx, &tasks, "SELECT * FROM bw_task WHERE object_key=? AND status<>'CLOSED'", objectKey); err != nil {
		return 0, err
	}
	closed := 0
	for i := range tasks {
		t := &tasks[i]
		cfg, err := s.Config(ctx, s.conn, t.TypeKey, t.ConfigVersion)
		if err != nil {
			continue
		}
		for _, ev := range cfg.Spec.Invalidation.Events {
			if ev == event {
				if err := s.terminate(ctx, t, CloseInvalidated, "system", "收到事件 "+event+"，自动关闭"); err == nil {
					closed++
				}
				break
			}
		}
	}
	return closed, nil
}

// terminate ends a task's run (listener closes the task and updates the card).
func (s *Service) terminate(ctx context.Context, t *Task, reason, actor, text string) error {
	if t.RunID == 0 {
		return nil
	}
	return s.eng.Locked(ctx, t.RunID, func() error {
		return s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
			s.log(ctx, q, t.ID, "close", actor, "%s", text)
			return s.eng.Terminate(ctx, q, t.RunID, map[string]any{"close_reason": reason})
		})
	})
}

// ---------- push ----------

type PushResult struct {
	Duplicate bool      `json:"duplicate"`
	TaskID    int64     `json:"task_id"`
	Created   bool      `json:"created"`
	Reason    string    `json:"reason,omitempty"`
	Candidate Candidate `json:"candidate"`
}

// Push accepts a business event for a task type (业务 Push). event_id makes
// redelivery idempotent; late or out-of-order events are harmless because the
// dedupe key and eligibility decide.
func (s *Service) Push(ctx context.Context, typeKey, eventID string, payload map[string]any) (*PushResult, error) {
	if eventID == "" {
		return nil, BadRequest("event_id 必填")
	}
	tt, err := s.Type(ctx, s.conn, typeKey)
	if err != nil {
		return nil, err
	}
	if _, err := s.conn.ExecCtx(ctx, "INSERT INTO bw_push_event (event_id, type_key, received_at, result) VALUES (?,?,?,?)", typeKey+":"+eventID, typeKey, s.nowMs(), "received"); err != nil {
		if db.IsDuplicate(err) {
			return &PushResult{Duplicate: true}, nil
		}
		return nil, err
	}
	res := &PushResult{}
	setResult := func(r string) {
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_push_event SET result=? WHERE event_id=?", trunc(r, 250), typeKey+":"+eventID)
	}
	if tt.Status != TypeEnabled && tt.Status != TypeShadow {
		res.Reason = "任务类型未启用"
		setResult(res.Reason)
		return res, nil
	}
	cfg, err := s.Config(ctx, s.conn, typeKey, tt.ActiveConfigVersion)
	if err != nil {
		return nil, err
	}
	if !cfg.Spec.Trigger.Has("push") {
		return nil, BadRequest("该任务类型未配置业务 Push 来源")
	}
	c := s.evaluate(ctx, tt, cfg.Spec, payload, tt.Status == TypeShadow)
	res.Candidate = c
	switch {
	case c.Error != "":
		res.Reason = c.Error
	case !c.Eligible:
		res.Reason = "资格条件不满足，不建任务"
	case c.Exists:
		res.Reason = "去重：任务已存在"
	default:
		id, err := s.createTask(ctx, tt, cfg.Version, c, Origin{Source: "push", At: s.nowMs(), EventID: eventID, ConfigVersion: cfg.Version, Matched: c.Matched})
		if err != nil {
			setResult("error: " + err.Error())
			return nil, err
		}
		res.TaskID, res.Created = id, id > 0
		res.Reason = "created"
	}
	setResult(res.Reason)
	return res, nil
}

// ---------- dry run / impact estimate ----------

type DryRun struct {
	OK        bool        `json:"ok"`
	Errors    []string    `json:"errors"`
	Columns   []string    `json:"columns"`
	Sample    []Candidate `json:"sample"`
	Rows      int         `json:"rows"`
	Eligible  int         `json:"eligible"`
	NewTasks  int         `json:"new_tasks"`
	NotifyRMs int         `json:"notify_rms"`
	Unique    bool        `json:"unique"`
	Checks    []string    `json:"checks"`
}

// DryRunConfig executes the (draft) config's query read-only, maps a sample,
// checks required fields and dedupe uniqueness, and estimates impact.
func (s *Service) DryRunConfig(ctx context.Context, tt *TaskType, spec *ConfigSpec, scanFrom int64) *DryRun {
	d := &DryRun{Unique: true, Errors: []string{}, Checks: []string{}, Sample: []Candidate{}, Columns: []string{}}
	if !spec.Trigger.Has("sql") {
		d.OK = true
		d.Checks = append(d.Checks, "无 SQL 来源，跳过试跑（Push 来源按字段映射校验）")
		return d
	}
	vars := expr.Vars{"now": s.nowMs(), "scan_from": scanFrom, "last_scan_at": int64(0)}
	if _, _, err := datasrc.Compile(spec.Trigger.Query, vars); err != nil {
		d.Errors = append(d.Errors, err.Error())
		return d
	}
	out, err := s.sources.Query(ctx, spec.Trigger.Datasource, spec.Trigger.Query, vars, 0)
	if err != nil {
		d.Errors = append(d.Errors, "查询失败: "+err.Error())
		return d
	}
	d.Columns, d.Rows = out.Columns, len(out.Rows)
	colSet := map[string]bool{}
	for _, c := range out.Columns {
		colSet[c] = true
	}
	for _, f := range spec.Fields {
		if !colSet[f.Column] {
			d.Errors = append(d.Errors, fmt.Sprintf("字段映射引用的列 %s 不在查询结果中", f.Column))
		}
	}
	seen := map[string]bool{}
	rms := map[string]bool{}
	for i, row := range out.Rows {
		c := s.evaluate(ctx, tt, spec, row, false)
		if i < 20 {
			d.Sample = append(d.Sample, c)
		}
		if c.DedupeKey != "" {
			if seen[c.DedupeKey] {
				d.Unique = false
			}
			seen[c.DedupeKey] = true
		}
		if c.Eligible {
			d.Eligible++
			if !c.Exists {
				d.NewTasks++
				if uid := c.Vars.String("case.uid"); uid != "" {
					if rm, _ := s.ResolveRM(ctx, s.conn, uid, tt.Team); rm != "" {
						rms[rm] = true
					}
				}
			}
		}
	}
	d.NotifyRMs = len(rms)
	if !d.Unique {
		d.Errors = append(d.Errors, "去重键在样本内不唯一")
	} else {
		d.Checks = append(d.Checks, "去重键在样本内唯一")
	}
	d.Checks = append(d.Checks, fmt.Sprintf("试跑通过 · 样本 %d 行", min(d.Rows, 20)), "只读 · 参数绑定通过")
	if spec.MaxPerRun > 0 && d.NewTasks > spec.MaxPerRun {
		d.Checks = append(d.Checks, fmt.Sprintf("注意：首次扫描预计新建 %d 个，超过单次上限 %d，将触发熔断", d.NewTasks, spec.MaxPerRun))
	}
	d.OK = len(d.Errors) == 0
	return d
}
