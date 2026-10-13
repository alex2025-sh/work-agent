package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/engine/nodes"
	"bywork/internal/expr"
)

type Options struct {
	Owner           string        // lease owner id (host/pod)
	LinkDomains     []string      // whitelist for card links / external jumps
	SignKey         string        // HMAC key for signed external URLs
	WebBaseURL      string        // "在 Bywork 中查看" link base
	DisplayTimezone string        // card time formatting
	ScanBudget      time.Duration // per-heartbeat time budget
	OutboxMaxTry    int
}

// Service is the task domain: task types, configs, scanning, tasks, work
// items, cards, delivery, and operations. It drives the engine but the engine
// does not depend on it.
type Service struct {
	conn    sqlx.SqlConn
	eng     *engine.Engine
	caps    *datasrc.Registry
	sources *datasrc.Sources
	clock   clock.Clock
	opt     Options
	sender  Sender
	kick    chan struct{}
	tz      *time.Location
}

func New(conn sqlx.SqlConn, eng *engine.Engine, caps *datasrc.Registry, sources *datasrc.Sources, c clock.Clock, sender Sender, opt Options) *Service {
	if opt.ScanBudget == 0 {
		opt.ScanBudget = 45 * time.Second
	}
	if opt.OutboxMaxTry == 0 {
		opt.OutboxMaxTry = 6
	}
	tz, err := time.LoadLocation(opt.DisplayTimezone)
	if err != nil || opt.DisplayTimezone == "" {
		tz = time.FixedZone("UTC+8", 8*3600)
	}
	s := &Service{conn: conn, eng: eng, caps: caps, sources: sources, clock: c, opt: opt, sender: sender, tz: tz}
	caps.Register(s.rmResolveCapability())
	eng.Register(nodes.Start{})
	eng.Register(nodes.End{})
	eng.Register(nodes.Wait{})
	eng.Register(nodes.Condition{})
	eng.Register(nodes.Data{Caps: caps})
	eng.Register(&Human{svc: s})
	eng.SetListener(&listener{svc: s})
	return s
}

func (s *Service) Engine() *engine.Engine    { return s.eng }
func (s *Service) Caps() *datasrc.Registry   { return s.caps }
func (s *Service) Sources() *datasrc.Sources { return s.sources }
func (s *Service) now() time.Time            { return s.clock.Now() }
func (s *Service) nowMs() int64              { return clock.Ms(s.clock.Now()) }

func (s *Service) fmtTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return clock.FromMs(ms).In(s.tz).Format("01/02 15:04")
}

// ---------- lookups ----------

func (s *Service) Type(ctx context.Context, q db.Querier, key string) (*TaskType, error) {
	var t TaskType
	if err := q.QueryRowCtx(ctx, &t, "SELECT * FROM bw_task_type WHERE type_key = ?", key); err != nil {
		if db.IsNotFound(err) {
			return nil, NotFound("任务类型 %s 不存在", key)
		}
		return nil, err
	}
	return &t, nil
}

func (s *Service) Task(ctx context.Context, q db.Querier, id int64) (*Task, error) {
	var t Task
	if err := q.QueryRowCtx(ctx, &t, "SELECT * FROM bw_task WHERE id = ?", id); err != nil {
		if db.IsNotFound(err) {
			return nil, NotFound("任务 %d 不存在", id)
		}
		return nil, err
	}
	return &t, nil
}

func (s *Service) Config(ctx context.Context, q db.Querier, key string, version int64) (*ConfigRow, error) {
	var c ConfigRow
	if err := q.QueryRowPartialCtx(ctx, &c, "SELECT * FROM bw_task_config WHERE type_key = ? AND version = ?", key, version); err != nil {
		if db.IsNotFound(err) {
			return nil, NotFound("配置 %s v%d 不存在", key, version)
		}
		return nil, err
	}
	return &c, c.Decode()
}

func (s *Service) Member(ctx context.Context, q db.Querier, user string) (*Member, error) {
	var m Member
	if err := q.QueryRowCtx(ctx, &m, "SELECT * FROM bw_team_member WHERE user_id = ?", user); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Service) memberName(ctx context.Context, q db.Querier, user string) string {
	if user == "" {
		return ""
	}
	if m, err := s.Member(ctx, q, user); err == nil && m.Name != "" {
		return m.Name
	}
	return user
}

func (s *Service) Members(ctx context.Context, team string) ([]Member, error) {
	out := []Member{}
	var err error
	if team == "" {
		err = s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_team_member ORDER BY team, user_id")
	} else {
		err = s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_team_member WHERE team = ? ORDER BY user_id", team)
	}
	return out, err
}

func (s *Service) workItems(ctx context.Context, q db.Querier, taskID int64) ([]WorkItem, error) {
	out := []WorkItem{}
	err := q.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_work_item WHERE task_id = ? ORDER BY id", taskID)
	return out, err
}

func (s *Service) activeWorkItem(ctx context.Context, q db.Querier, taskID int64) (*WorkItem, error) {
	var w WorkItem
	err := q.QueryRowCtx(ctx, &w, "SELECT * FROM bw_work_item WHERE task_id = ? AND status = 'active' ORDER BY id DESC LIMIT 1", taskID)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ---------- logging / audit ----------

func (s *Service) log(ctx context.Context, q db.Querier, taskID int64, kind, actor, text string, args ...any) {
	if len(args) > 0 {
		text = fmt.Sprintf(text, args...)
	}
	_, _ = q.ExecCtx(ctx, "INSERT INTO bw_task_log (task_id, at, kind, actor, text) VALUES (?,?,?,?,?)", taskID, s.nowMs(), kind, actor, trunc(text, 1000))
}

func (s *Service) Audit(ctx context.Context, q db.Querier, actor, action, target string, detail any) {
	_, _ = q.ExecCtx(ctx, "INSERT INTO bw_audit (at, actor, action, target, detail) VALUES (?,?,?,?,?)", s.nowMs(), actor, action, target, db.JSON(detail))
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------- RM resolution: data.rm.resolve ----------

// ResolveRM applies, in order: active approved override, then the
// authoritative UID→RM mapping. Inactive members are treated as missing.
func (s *Service) ResolveRM(ctx context.Context, q db.Querier, uid, team string) (string, AssignReason) {
	reason := AssignReason{Rule: "VIP_UID_TO_RM", Team: team}
	now := s.nowMs()
	var ov Override
	if err := q.QueryRowCtx(ctx, &ov, "SELECT * FROM bw_rm_override WHERE uid = ? AND revoked = 0 AND valid_until > ? ORDER BY id DESC LIMIT 1", uid, now); err == nil {
		if m, err := s.Member(ctx, q, ov.RMUser); err == nil && m.Active == 1 {
			reason.Via, reason.OverrideID, reason.Approval = "override", ov.ID, ov.ApprovalNo
			return ov.RMUser, reason
		}
	}
	var rm string
	if err := q.QueryRowCtx(ctx, &rm, "SELECT rm_user FROM demo_vip WHERE uid = ?", uid); err == nil && rm != "" {
		if m, err := s.Member(ctx, q, rm); err == nil && m.Active == 1 && (team == "" || m.Team == team) {
			reason.Via = "mapping"
			return rm, reason
		}
		reason.Note = "映射到的 RM " + rm + " 已不在团队"
	}
	reason.Via = "missing"
	return "", reason
}

func (s *Service) rmResolveCapability() *datasrc.Capability {
	return &datasrc.Capability{
		Name: "data.rm.resolve", Title: "解析当前 RM", Desc: "临时例外优先，其次权威 UID→RM 映射",
		Args: []string{"uid"}, Outputs: []string{"rm.user_id", "rm.name", "rm.via"},
		Call: func(ctx context.Context, q db.Querier, args map[string]string) (map[string]any, error) {
			u, r := s.ResolveRM(ctx, q, args["uid"], args["team"])
			return map[string]any{"rm.user_id": u, "rm.name": s.memberName(ctx, q, u), "rm.via": r.Via}, nil
		},
	}
}

// ---------- signed links ----------

func (s *Service) allowedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	for _, d := range s.opt.LinkDomains {
		if u.Hostname() == d || strings.HasSuffix(u.Hostname(), "."+d) {
			return true
		}
	}
	return false
}

func (s *Service) sign(raw, user string, taskID int64) string {
	exp := s.now().Add(10 * time.Minute).Unix()
	mac := hmac.New(sha256.New, []byte(s.opt.SignKey))
	fmt.Fprintf(mac, "%s|%s|%d|%d", raw, user, taskID, exp)
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sbw_task=%d&bw_user=%s&bw_exp=%d&bw_sig=%s", raw, sep, taskID, url.QueryEscape(user), exp, hex.EncodeToString(mac.Sum(nil))[:32])
}

// ---------- revalidation ----------

// Revalidate re-reads the business object via the config's live_refresh
// capabilities and re-checks eligibility of the config version the task was
// created with. Used before delayed reminders and by invalidation rechecks.
func (s *Service) Revalidate(ctx context.Context, q db.Querier, t *Task, vars expr.Vars) (bool, expr.Vars, error) {
	cfg, err := s.Config(ctx, q, t.TypeKey, t.ConfigVersion)
	if err != nil {
		return false, nil, err
	}
	live := vars.Clone()
	for _, r := range cfg.Spec.LiveRefresh {
		cp, ok := s.caps.Get(r.Capability)
		if !ok {
			return false, nil, fmt.Errorf("unknown capability %s", r.Capability)
		}
		args := map[string]string{}
		for k, v := range r.Args {
			args[k] = expr.Render(v, live)
		}
		out, err := cp.Call(ctx, q, args)
		if err != nil {
			return false, nil, err
		}
		live.Merge(out)
	}
	ok, _, err := cfg.Spec.Eligibility.Eval(live, s.now())
	if err != nil {
		return false, nil, err
	}
	_, _ = q.ExecCtx(ctx, "UPDATE bw_task SET last_checked_at=? WHERE id=?", s.nowMs(), t.ID)
	return ok, live, nil
}

// ---------- errors ----------

type Error struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Msg }

func NotFound(f string, a ...any) error   { return &Error{Code: 404, Msg: fmt.Sprintf(f, a...)} }
func BadRequest(f string, a ...any) error { return &Error{Code: 400, Msg: fmt.Sprintf(f, a...)} }
func Forbidden(f string, a ...any) error  { return &Error{Code: 403, Msg: fmt.Sprintf(f, a...)} }
func Conflict(data any, f string, a ...any) error {
	return &Error{Code: 409, Msg: fmt.Sprintf(f, a...), Data: data}
}
