package task

import (
	"context"

	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// listener keeps Task state in step with the run, inside the engine's tx.
type listener struct{ svc *Service }

func (l *listener) OnRunEnd(ctx context.Context, q db.Querier, run *engine.Run, result map[string]any) error {
	if run.TaskID == 0 {
		return nil // automation flows (巡检) have no task
	}
	s := l.svc
	reason := expr.Vars(result).String("close_reason")
	if reason == "" {
		reason = CloseCompleted
	}
	now := s.nowMs()
	if _, err := q.ExecCtx(ctx, "UPDATE bw_work_item SET status='voided', done_at=? WHERE task_id=? AND status='active'", now, run.TaskID); err != nil {
		return err
	}
	n, err := db.Affected(ctx, q, "UPDATE bw_task SET status=?, close_reason=?, closed_at=?, next_action_at=0, next_action_text='', attention='', updated_at=? WHERE id=? AND status<>?",
		StatusClosed, reason, now, now, run.TaskID, StatusClosed)
	if err != nil || n == 0 {
		return err
	}
	s.log(ctx, q, run.TaskID, "close", "system", "任务关闭 · %s", CloseReasonText[reason])
	return s.syncCard(ctx, q, run.TaskID)
}

func (l *listener) OnRunError(ctx context.Context, q db.Querier, run *engine.Run, nodeKey string, cause error) error {
	if run.TaskID == 0 {
		return nil
	}
	s := l.svc
	flag := AttnFlowError
	if def, err := s.eng.Definition(ctx, q, run.FlowKey, run.FlowVersion); err == nil {
		if n := def.Graph.Node(nodeKey); n != nil && n.Type == "data" {
			flag = AttnReadFailed
		}
	}
	t, err := s.Task(ctx, q, run.TaskID)
	if err != nil {
		return err
	}
	if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET attention=?, updated_at=? WHERE id=?", withFlag(t.Attention, flag, true), s.nowMs(), t.ID); err != nil {
		return err
	}
	s.log(ctx, q, t.ID, "error", "system", "节点 %s 执行失败，已挂起等待处理：%s", nodeKey, cause.Error())
	return nil
}

func (l *listener) OnSuspend(ctx context.Context, q db.Querier, run *engine.Run, node *engine.Node, timers []engine.TimerSpec) error {
	if run.TaskID == 0 || node.Type != "wait" || len(timers) == 0 {
		return nil
	}
	s := l.svc
	due := timers[0].DueAt
	for _, t := range timers[1:] {
		if t.DueAt.Before(due) {
			due = t.DueAt
		}
	}
	text := s.fmtTime(due.UnixMilli()) + " " + node.Name
	if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET status=?, next_action_at=?, next_action_text=?, updated_at=? WHERE id=?",
		StatusOpen, due.UnixMilli(), text, s.nowMs(), run.TaskID); err != nil {
		return err
	}
	s.log(ctx, q, run.TaskID, "wait", "system", "等待：%s", text)
	return s.syncCard(ctx, q, run.TaskID)
}
