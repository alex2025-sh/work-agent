package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// Viewer is the authenticated caller; every action re-checks permissions
// server side (client-supplied assignee/team are only filters).
type Viewer struct {
	UserID string
	Role   string // rm | leader | config | admin
	Team   string
}

func (v Viewer) IsLeaderOf(team string) bool {
	return v.Role == "admin" || (v.Role == "leader" && v.Team == team)
}

func (v Viewer) CanSeeSensitive() bool {
	return v.Role == "rm" || v.Role == "leader" || v.Role == "admin"
}

type ActionReq struct {
	WorkItemID int64  `json:"work_item_id"`
	Button     string `json:"button"`
	Note       string `json:"note"`
	RequestKey string `json:"request_key"`
	CardRev    int64  `json:"card_rev"`
}

// Act applies a card button. Idempotency key = action:<request_key>: same key
// + same params returns the original result, a different param is rejected.
// A click on an outdated card revision is rejected with the latest card.
func (s *Service) Act(ctx context.Context, v Viewer, taskID int64, r ActionReq) (*engine.EventResult, error) {
	if r.RequestKey == "" {
		return nil, BadRequest("request_key 必填")
	}
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return nil, err
	}
	var wi WorkItem
	if err := s.conn.QueryRowCtx(ctx, &wi, "SELECT * FROM bw_work_item WHERE id=? AND task_id=?", r.WorkItemID, taskID); err != nil {
		return nil, NotFound("待办不存在")
	}
	if wi.Assignee != v.UserID && !v.IsLeaderOf(t.Team) {
		return nil, Forbidden("你不是这条待办的处理人")
	}
	payload := map[string]any{"work_item_id": wi.ID, "button": r.Button, "note": r.Note, "actor": v.UserID}
	ev := engine.Event{Kind: "action", Key: "action:" + r.RequestKey, NodeKey: wi.NodeKey, Payload: payload}
	// replays of an already-applied request return the original result
	if res, err := s.replay(ctx, t.RunID, ev); res != nil || err != nil {
		return res, err
	}
	if wi.Status != "active" || r.CardRev != wi.CardRev {
		view, _ := s.cardView(ctx, s.conn, t)
		return nil, Conflict(view, "卡片已更新，请按最新内容操作")
	}
	btns := []HumanButton{}
	_ = json.Unmarshal([]byte(wi.ButtonsJSON), &btns)
	var btn *HumanButton
	for i := range btns {
		if btns[i].Key == r.Button {
			btn = &btns[i]
		}
	}
	if btn == nil {
		return nil, BadRequest("按钮 %s 不可用", r.Button)
	}
	if btn.Kind == "external" && !btn.CompleteRound {
		return nil, BadRequest("外跳按钮请使用 open-link")
	}
	card := decodeCard(wi.CardJSON)
	for _, b := range card.Buttons {
		if b.Key == r.Button && b.NoteRequired && strings.TrimSpace(r.Note) == "" {
			return nil, BadRequest("该操作需要填写备注")
		}
	}
	res, err := s.eng.Deliver(ctx, t.RunID, ev, 3*time.Second)
	if errors.Is(err, engine.ErrKeyConflict) {
		return nil, Conflict(nil, "%s", err.Error())
	}
	if errors.Is(err, engine.ErrBusy) {
		return nil, Conflict(nil, "任务正在处理中，请稍后重试")
	}
	return res, err
}

// replay returns the stored result when ev.Key was already applied.
func (s *Service) replay(ctx context.Context, runID int64, ev engine.Event) (*engine.EventResult, error) {
	var n int64
	_ = s.conn.QueryRowCtx(ctx, &n, "SELECT COUNT(1) FROM wf_event WHERE run_id=? AND event_key=?", runID, ev.Key)
	if n == 0 {
		return nil, nil
	}
	res, err := s.eng.Deliver(ctx, runID, ev, 0)
	if errors.Is(err, engine.ErrKeyConflict) {
		return nil, Conflict(nil, "%s", err.Error())
	}
	return res, err
}

type OpenLinkReq struct {
	WorkItemID int64  `json:"work_item_id"`
	Button     string `json:"button"`
	RequestKey string `json:"request_key"`
	CardRev    int64  `json:"card_rev"`
}

type OpenLinkResp struct {
	URL       string `json:"url"`
	Completed bool   `json:"completed"`
}

// OpenLink checks the caller is the current assignee, issues a signed URL and
// records "已打开". It never means the external process was completed.
func (s *Service) OpenLink(ctx context.Context, v Viewer, taskID int64, r OpenLinkReq) (*OpenLinkResp, error) {
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return nil, err
	}
	var wi WorkItem
	if err := s.conn.QueryRowCtx(ctx, &wi, "SELECT * FROM bw_work_item WHERE id=? AND task_id=?", r.WorkItemID, taskID); err != nil {
		return nil, NotFound("待办不存在")
	}
	if wi.Assignee != v.UserID && !v.IsLeaderOf(t.Team) {
		return nil, Forbidden("只有当前处理人可以外跳")
	}
	btns := []HumanButton{}
	_ = json.Unmarshal([]byte(wi.ButtonsJSON), &btns)
	var btn *HumanButton
	for i := range btns {
		if btns[i].Key == r.Button && btns[i].Kind == "external" {
			btn = &btns[i]
		}
	}
	if btn == nil {
		return nil, BadRequest("外跳按钮不存在")
	}
	raw := expr.Render(btn.URL, t.Snapshot())
	if !s.allowedURL(raw) {
		return nil, Forbidden("外跳域名不在白名单")
	}
	s.log(ctx, s.conn, t.ID, "open_link", v.UserID, "%s 点击「%s」· 已打开（外部流程结果不会回到 Bywork）", s.memberName(ctx, s.conn, v.UserID), btn.Label)
	s.Audit(ctx, s.conn, v.UserID, "open_link", itoa(taskID), map[string]any{"button": btn.Key})
	out := &OpenLinkResp{URL: s.sign(raw, v.UserID, t.ID)}
	if btn.CompleteRound && wi.Status == "active" {
		if _, err := s.Act(ctx, v, taskID, ActionReq{WorkItemID: wi.ID, Button: btn.Key, RequestKey: r.RequestKey, CardRev: r.CardRev}); err != nil {
			return out, err
		}
		out.Completed = true
	}
	return out, nil
}

// Reassign (Leader 改派 / 指派) moves the active work item to another member.
func (s *Service) Reassign(ctx context.Context, v Viewer, taskID int64, to, note string) error {
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return err
	}
	if !v.IsLeaderOf(t.Team) {
		return Forbidden("只有团队 Leader 可以改派")
	}
	m, err := s.Member(ctx, s.conn, to)
	if err != nil || m.Active != 1 || m.Team != t.Team {
		return BadRequest("%s 不是团队 %s 的在职成员", to, t.Team)
	}
	return s.eng.Locked(ctx, t.RunID, func() error {
		return s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
			wi, err := s.activeWorkItem(ctx, q, taskID)
			if err != nil {
				return BadRequest("当前没有待处理的待办可改派")
			}
			reason := AssignReason{Rule: "leader", Team: t.Team, Via: "leader", By: v.UserID, Note: note}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_work_item SET assignee=?, assign_reason=? WHERE id=?", to, db.JSON(reason), wi.ID); err != nil {
				return err
			}
			att := withFlag(withFlag(t.Attention, AttnRMMissing, false), AttnAssigneeLeft, false)
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET assignee=?, status=?, attention=?, updated_at=? WHERE id=?", to, StatusProcessing, att, s.nowMs(), taskID); err != nil {
				return err
			}
			s.log(ctx, q, taskID, "ops", v.UserID, "%s 将待办改派给 %s%s", s.memberName(ctx, q, v.UserID), m.Name, notePart(note))
			s.Audit(ctx, q, v.UserID, "reassign", itoa(taskID), map[string]any{"to": to, "note": note})
			return s.syncCard(ctx, q, taskID)
		})
	})
}

// Cancel (取消) terminates the task as 管理员终止.
func (s *Service) Cancel(ctx context.Context, v Viewer, taskID int64, note string) error {
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return err
	}
	if !v.IsLeaderOf(t.Team) {
		return Forbidden("只有 Leader 或管理员可以取消任务")
	}
	if t.Status == StatusClosed {
		return BadRequest("任务已结束")
	}
	s.Audit(ctx, s.conn, v.UserID, "cancel", itoa(taskID), map[string]any{"note": note})
	return s.terminate(ctx, t, CloseAdmin, v.UserID, s.memberName(ctx, s.conn, v.UserID)+" 取消任务"+notePart(note))
}

// Retrigger (重新触发) re-runs the step that failed (e.g. 数据读取失败).
func (s *Service) Retrigger(ctx context.Context, v Viewer, taskID int64) error {
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return err
	}
	if !v.IsLeaderOf(t.Team) {
		return Forbidden("只有 Leader 或管理员可以重新触发")
	}
	att := withFlag(withFlag(t.Attention, AttnReadFailed, false), AttnFlowError, false)
	if _, err := s.conn.ExecCtx(ctx, "UPDATE bw_task SET attention=? WHERE id=?", att, taskID); err != nil {
		return err
	}
	s.log(ctx, s.conn, taskID, "ops", v.UserID, "%s 重新触发", s.memberName(ctx, s.conn, v.UserID))
	s.Audit(ctx, s.conn, v.UserID, "retrigger", itoa(taskID), nil)
	if err := s.eng.Retry(ctx, t.RunID); err != nil {
		return BadRequest("%v", err)
	}
	return nil
}

// Feedback: "不该给我 / 不该存在" — feeds the 误报率 metric.
func (s *Service) Feedback(ctx context.Context, v Viewer, taskID int64, kind, note string) error {
	if kind != "not_mine" && kind != "should_not_exist" {
		return BadRequest("kind 只能是 not_mine 或 should_not_exist")
	}
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return err
	}
	if _, err := s.conn.ExecCtx(ctx, "INSERT INTO bw_feedback (task_id, type_key, user_id, kind, note, created_at) VALUES (?,?,?,?,?,?)",
		taskID, t.TypeKey, v.UserID, kind, note, s.nowMs()); err != nil {
		return err
	}
	label := map[string]string{"not_mine": "不该给我", "should_not_exist": "不该存在"}[kind]
	s.log(ctx, s.conn, taskID, "feedback", v.UserID, "%s 反馈「%s」%s", s.memberName(ctx, s.conn, v.UserID), label, notePart(note))
	return nil
}

func notePart(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "：" + note
}

// ---------- temporary RM overrides ----------

type OverrideReq struct {
	UID        string `json:"uid"`
	RMUser     string `json:"rm_user"`
	ApprovalNo string `json:"approval_no"`
	ValidUntil int64  `json:"valid_until"`
}

func (s *Service) Overrides(ctx context.Context) ([]Override, error) {
	out := []Override{}
	err := s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_rm_override WHERE revoked=0 AND valid_until > ? ORDER BY id DESC", s.nowMs())
	return out, err
}

// AddOverride: takes precedence over the authoritative mapping, requires an
// approval number, and expires automatically.
func (s *Service) AddOverride(ctx context.Context, v Viewer, r OverrideReq) error {
	if v.Role != "leader" && v.Role != "admin" {
		return Forbidden("只有 Leader 可以设置临时 RM 例外")
	}
	if r.UID == "" || r.RMUser == "" || r.ApprovalNo == "" || r.ValidUntil <= s.nowMs() {
		return BadRequest("UID、RM、审批单号、有效期必填，且有效期需在未来")
	}
	if m, err := s.Member(ctx, s.conn, r.RMUser); err != nil || m.Active != 1 {
		return BadRequest("RM %s 不是在职成员", r.RMUser)
	}
	if _, err := s.conn.ExecCtx(ctx, "INSERT INTO bw_rm_override (uid, rm_user, approval_no, valid_until, created_by, created_at) VALUES (?,?,?,?,?,?)",
		r.UID, r.RMUser, r.ApprovalNo, r.ValidUntil, v.UserID, s.nowMs()); err != nil {
		return err
	}
	s.Audit(ctx, s.conn, v.UserID, "add_override", r.UID, r)
	return nil
}

func (s *Service) RevokeOverride(ctx context.Context, v Viewer, id int64) error {
	if v.Role != "leader" && v.Role != "admin" {
		return Forbidden("只有 Leader 可以撤销例外")
	}
	if _, err := s.conn.ExecCtx(ctx, "UPDATE bw_rm_override SET revoked=1 WHERE id=?", id); err != nil {
		return err
	}
	s.Audit(ctx, s.conn, v.UserID, "revoke_override", itoa(id), nil)
	return nil
}

// SetMemberActive handles 离职 / 移出团队: their active work items go to the
// leader's 需要关注 instead of staying in an invisible inbox.
func (s *Service) SetMemberActive(ctx context.Context, v Viewer, user string, active bool) error {
	m, err := s.Member(ctx, s.conn, user)
	if err != nil {
		return NotFound("成员不存在")
	}
	if !v.IsLeaderOf(m.Team) {
		return Forbidden("只有团队 Leader 可以调整成员")
	}
	a := 0
	if active {
		a = 1
	}
	return s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
		if _, err := q.ExecCtx(ctx, "UPDATE bw_team_member SET active=? WHERE user_id=?", a, user); err != nil {
			return err
		}
		if !active {
			var tasks []Task
			if err := q.QueryRowsCtx(ctx, &tasks, "SELECT t.* FROM bw_task t WHERE t.status<>'CLOSED' AND t.id IN (SELECT task_id FROM bw_work_item WHERE assignee=? AND status='active')", user); err != nil {
				return err
			}
			for _, t := range tasks {
				if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET attention=? WHERE id=?", withFlag(t.Attention, AttnAssigneeLeft, true), t.ID); err != nil {
					return err
				}
				s.log(ctx, q, t.ID, "ops", v.UserID, "处理人 %s 已离开团队，待办进入 Leader「需要关注」", m.Name)
			}
		}
		s.Audit(ctx, q, v.UserID, "member_active", user, map[string]any{"active": active})
		return nil
	})
}
