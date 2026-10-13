package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// HumanConfig configures a 人工处理 node.
type HumanConfig struct {
	Assignee   AssigneeRule  `json:"assignee"`
	Card       string        `json:"card"`                        // card template key
	Title      string        `json:"title"`                       // overrides template title
	RoundLabel string        `json:"round_label"`                 // e.g. "48 小时" / "7 天"
	LaterTitle string        `json:"later_title,omitempty"`       // title for rounds > 1
	LaterLabel string        `json:"later_round_label,omitempty"` // badge for rounds > 1, e.g. "T+3"
	Buttons    []HumanButton `json:"buttons"`
	Delay      *DelayConfig  `json:"delay,omitempty"`
	Deadline   *Deadline     `json:"deadline,omitempty"`
}

type AssigneeRule struct {
	Team     string `json:"team"`
	Rule     string `json:"rule"` // VIP_UID_TO_RM | field | fixed
	UIDField string `json:"uid_field"`
	Field    string `json:"field"` // rule=field: a var holding the user id (e.g. rm.user_id)
	User     string `json:"user"`  // rule=fixed
}

// HumanButton: kind complete finishes the round via Exit; external records
// "opened" (and completes the round when CompleteRound); delay postpones.
type HumanButton struct {
	Key            string `json:"key"`
	Label          string `json:"label"`
	Kind           string `json:"kind"` // complete | external | delay
	Exit           string `json:"exit,omitempty"`
	URL            string `json:"url,omitempty"`
	CompleteRound  bool   `json:"complete_round,omitempty"`
	FirstRoundOnly bool   `json:"first_round_only,omitempty"`
}

// DelayConfig: "No Response" = 延迟处理. RemindAt is a time expression (需求一:
// case.created_at + 3d). If it has already passed when clicked, re-check now.
type DelayConfig struct {
	RemindAt   string `json:"remind_at"`
	Max        int    `json:"max"`
	Revalidate bool   `json:"revalidate"`
}

// Deadline (节点定时): when still unfinished at At, void the round and take Exit.
type Deadline struct {
	At   string `json:"at"`
	Exit string `json:"exit"`
}

type Human struct{ svc *Service }

const humanSchema = `{"type":"object","properties":{
 "assignee":{"type":"object","title":"处理人","properties":{
   "team":{"type":"string","title":"团队"},
   "rule":{"type":"string","title":"规则","enum":["VIP_UID_TO_RM","field","fixed"],"enumNames":["按 UID 解析 RM","取字段","固定人员"]},
   "uid_field":{"type":"string","title":"UID 字段","default":"case.uid"},
   "field":{"type":"string","title":"处理人字段"},
   "user":{"type":"string","title":"固定处理人"}}},
 "card":{"type":"string","title":"卡片模板","x-widget":"card"},
 "title":{"type":"string","title":"标题覆盖"},
 "round_label":{"type":"string","title":"轮次标签"},
 "later_title":{"type":"string","title":"后续轮次标题"},
 "later_round_label":{"type":"string","title":"后续轮次标签"},
 "buttons":{"type":"array","title":"按钮与出口","items":{"type":"object","properties":{
   "key":{"type":"string","title":"key"},
   "label":{"type":"string","title":"按钮文字"},
   "kind":{"type":"string","title":"类型","enum":["complete","external","delay"],"enumNames":["完成本轮","外跳","延迟处理"]},
   "exit":{"type":"string","title":"出口"},
   "url":{"type":"string","title":"外跳链接"},
   "complete_round":{"type":"boolean","title":"外跳同时完成本轮"},
   "first_round_only":{"type":"boolean","title":"仅第一轮显示"}}}},
 "delay":{"type":"object","title":"延迟处理","properties":{
   "remind_at":{"type":"string","title":"再次提醒时间","description":"如 case.created_at + 3d"},
   "max":{"type":"integer","title":"最多延迟次数","default":1},
   "revalidate":{"type":"boolean","title":"唤醒前重新校验资格","default":true}}},
 "deadline":{"type":"object","title":"节点定时","properties":{
   "at":{"type":"string","title":"到期时间","description":"如 case.stage_entered_at + 7d"},
   "exit":{"type":"string","title":"超时出口","default":"timeout"}}}}}`

func (h *Human) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "human", Title: "人工处理", Category: "phase1", Desc: "生成待办和卡片：处理人、按钮与出口、延迟、节点定时", Color: "#0d6b4f",
		Schema: json.RawMessage(humanSchema)}
}

func decodeHuman(n *engine.Node) (*HumanConfig, error) {
	c := &HumanConfig{}
	if len(n.Config) == 0 {
		return c, nil
	}
	return c, json.Unmarshal(n.Config, c)
}

func (h *Human) ValidateConfig(n *engine.Node, vc *engine.ValidateCtx) []engine.FieldError {
	c, err := decodeHuman(n)
	if err != nil {
		return []engine.FieldError{{Message: "配置格式错误: " + err.Error()}}
	}
	out := []engine.FieldError{}
	add := func(field, msg string, a ...any) {
		out = append(out, engine.FieldError{Field: field, Message: fmt.Sprintf(msg, a...)})
	}
	switch c.Assignee.Rule {
	case "VIP_UID_TO_RM":
		if c.Assignee.UIDField == "" {
			add("assignee.uid_field", "按 UID 解析需要 UID 字段")
		}
	case "field":
		if c.Assignee.Field == "" {
			add("assignee.field", "需要处理人字段")
		}
	case "fixed":
		if c.Assignee.User == "" {
			add("assignee.user", "需要固定处理人")
		}
	default:
		add("assignee.rule", "未知处理人规则 %q", c.Assignee.Rule)
	}
	if c.Card == "" {
		add("card", "需要卡片模板")
	} else if cards, ok := vc.Extra["cards"].(map[string]CardTemplate); ok {
		if _, ok := cards[c.Card]; !ok {
			add("card", "卡片模板 %s 不存在", c.Card)
		}
	}
	if len(c.Buttons) == 0 {
		add("buttons", "至少需要一个按钮")
	}
	keys := map[string]bool{}
	hasDelay := false
	for _, b := range c.Buttons {
		if b.Key == "" || keys[b.Key] {
			add("buttons", "按钮 key 为空或重复: %q", b.Key)
		}
		keys[b.Key] = true
		switch b.Kind {
		case "complete":
			if b.Exit == "" {
				add("buttons", "按钮 %s 需要出口", b.Key)
			}
		case "external":
			if b.URL == "" {
				add("buttons", "外跳按钮 %s 需要链接", b.Key)
			} else if !h.svc.allowedURL(expr.Render(b.URL, expr.Vars{})) && !strings.Contains(b.URL, "{{") {
				add("buttons", "外跳链接 %s 不在域名白名单", b.URL)
			}
			if b.CompleteRound && b.Exit == "" {
				add("buttons", "外跳按钮 %s 完成本轮时需要出口", b.Key)
			}
		case "delay":
			hasDelay = true
		default:
			add("buttons", "按钮 %s 类型未知: %q", b.Key, b.Kind)
		}
	}
	if hasDelay {
		if c.Delay == nil || c.Delay.RemindAt == "" {
			add("delay.remind_at", "有延迟按钮时需要配置再次提醒时间")
		} else if err := expr.TimeExpr(c.Delay.RemindAt).Validate(); err != nil {
			add("delay.remind_at", "%v", err)
		}
	}
	if c.Deadline != nil && c.Deadline.At != "" {
		if err := expr.TimeExpr(c.Deadline.At).Validate(); err != nil {
			add("deadline.at", "%v", err)
		}
	}
	for _, b := range c.Buttons {
		if b.Kind == "external" {
			out = append(out, engine.FieldError{Level: "info", Field: "buttons", Message: "提示 · 外跳出口不能作为「外部已提交」的判断依据"})
			break
		}
	}
	return out
}

func (h *Human) Exits(n *engine.Node) []string {
	c, _ := decodeHuman(n)
	out := []string{}
	seen := map[string]bool{}
	for _, b := range c.Buttons {
		if b.Exit != "" && (b.Kind == "complete" || b.CompleteRound) && !seen[b.Exit] {
			seen[b.Exit] = true
			out = append(out, b.Exit)
		}
	}
	if c.Deadline != nil && c.Deadline.At != "" {
		e := c.Deadline.Exit
		if e == "" {
			e = "timeout"
		}
		if !seen[e] {
			out = append(out, e)
		}
	}
	return out
}

func (h *Human) Fields(n *engine.Node) ([]string, []string) {
	c, _ := decodeHuman(n)
	var reads []string
	if c.Assignee.Rule == "VIP_UID_TO_RM" {
		reads = append(reads, c.Assignee.UIDField)
	}
	if c.Assignee.Rule == "field" {
		reads = append(reads, c.Assignee.Field)
	}
	reads = append(reads, expr.TemplateFields(c.Title)...)
	for _, b := range c.Buttons {
		reads = append(reads, expr.TemplateFields(b.URL)...)
	}
	if c.Delay != nil {
		reads = append(reads, expr.TimeExpr(c.Delay.RemindAt).Fields()...)
	}
	if c.Deadline != nil {
		reads = append(reads, expr.TimeExpr(c.Deadline.At).Fields()...)
	}
	// card fields are added by the host when computing the contract (cards
	// live next to the graph, see Service.FlowContract).
	return reads, nil
}

func (h *Human) state(in *engine.NodeInput) (round, delays, workItem int64) {
	f := func(k string) int64 {
		x, _ := expr.ToFloat(in.State[k])
		return int64(x)
	}
	return f("round"), f("delays"), f("work_item_id")
}

func (h *Human) Execute(ctx context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, err := decodeHuman(in.Node)
	if err != nil {
		return engine.NodeResult{}, err
	}
	var timers []engine.TimerSpec
	if c.Deadline != nil && c.Deadline.At != "" {
		due, err := expr.TimeExpr(c.Deadline.At).Eval(in.Vars, in.Now)
		if err != nil {
			return engine.NodeResult{}, err
		}
		if !due.After(in.Now) {
			// already past: never send an expired card
			return engine.NodeResult{Exit: deadlineExit(c)}, nil
		}
		timers = append(timers, engine.TimerSpec{Key: "deadline", DueAt: due})
	}
	res, err := h.newRound(ctx, in, c, 1, 0)
	if err != nil {
		return res, err
	}
	res.Suspend.Timers = append(res.Suspend.Timers, timers...)
	return res, nil
}

func deadlineExit(c *HumanConfig) string {
	if c.Deadline != nil && c.Deadline.Exit != "" {
		return c.Deadline.Exit
	}
	return "timeout"
}

// newRound creates the work item + card for a round and delivers it.
func (h *Human) newRound(ctx context.Context, in *engine.NodeInput, c *HumanConfig, round, delays int64) (engine.NodeResult, error) {
	s := h.svc
	t, err := s.Task(ctx, in.Q, in.Run.TaskID)
	if err != nil {
		return engine.NodeResult{}, err
	}
	tt, err := s.Type(ctx, in.Q, t.TypeKey)
	if err != nil {
		return engine.NodeResult{}, err
	}
	// re-resolve the assignee before every round (RM may have changed)
	var assignee string
	var reason AssignReason
	switch c.Assignee.Rule {
	case "VIP_UID_TO_RM":
		assignee, reason = s.ResolveRM(ctx, in.Q, in.Vars.String(c.Assignee.UIDField), c.Assignee.Team)
	case "field":
		assignee, reason = in.Vars.String(c.Assignee.Field), AssignReason{Rule: "field:" + c.Assignee.Field, Team: c.Assignee.Team, Via: "field"}
	case "fixed":
		assignee, reason = c.Assignee.User, AssignReason{Rule: "fixed", Team: c.Assignee.Team, Via: "fixed"}
	}
	if assignee != "" {
		if m, err := s.Member(ctx, in.Q, assignee); err != nil || m.Active != 1 {
			reason.Note = "处理人 " + assignee + " 不在团队"
			assignee, reason.Via = "", "missing"
		}
	}

	cfg, err := s.Config(ctx, in.Q, t.TypeKey, t.ConfigVersion)
	if err != nil {
		return engine.NodeResult{}, err
	}
	tpl := cardTemplates(in.Def)[c.Card]
	buttons := visibleButtons(c, round, delays)
	card := s.renderCard(tpl, c, in.Vars, round, buttons, cfg.Spec.Sensitive())
	rev := t.CardRev + 1
	now := s.nowMs()
	wiID, err := db.Insert(ctx, in.Q, "INSERT INTO bw_work_item (task_id, run_id, node_key, round, assignee, assign_reason, status, card, card_rev, buttons, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		t.ID, in.Run.ID, in.Node.Key, round, assignee, db.JSON(reason), "active", db.JSON(card), rev, db.JSON(buttons), now)
	if err != nil {
		return engine.NodeResult{}, err
	}
	status, attention := StatusProcessing, withFlag(t.Attention, AttnRMMissing, assignee == "")
	attention = withFlag(attention, AttnAssigneeLeft, false)
	if assignee == "" {
		status = StatusOpen // waiting for a leader to assign
	}
	if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_task SET status=?, assignee=?, attention=?, card_rev=?, next_action_at=0, next_action_text='', updated_at=? WHERE id=?",
		status, assignee, attention, rev, now, t.ID); err != nil {
		return engine.NodeResult{}, err
	}
	t.Status, t.Assignee, t.Attention, t.CardRev = status, assignee, attention, rev
	label := strings.TrimSpace(in.Node.Name)
	if assignee == "" {
		s.log(ctx, in.Q, t.ID, "assign", "system", "「%s」第 %d 轮：RM 映射缺失，进入 Leader「需要关注」", label, round)
	} else {
		s.log(ctx, in.Q, t.ID, "deliver", "system", "「%s」第 %d 轮投递给 %s · 卡片 r%d", label, round, s.memberName(ctx, in.Q, assignee), rev)
		if err := s.enqueueCard(ctx, in.Q, t, tt); err != nil {
			return engine.NodeResult{}, err
		}
	}
	return engine.NodeResult{
		State:   map[string]any{"round": round, "delays": delays, "work_item_id": wiID},
		Output:  map[string]any{"node.round_label": c.RoundLabel},
		Suspend: &engine.Suspend{Reason: "human"},
	}, nil
}

func visibleButtons(c *HumanConfig, round, delays int64) []HumanButton {
	out := []HumanButton{}
	for _, b := range c.Buttons {
		if b.FirstRoundOnly && round > 1 {
			continue
		}
		if b.Kind == "delay" {
			max := int64(1)
			if c.Delay != nil && c.Delay.Max > 0 {
				max = int64(c.Delay.Max)
			}
			if delays >= max {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

func (h *Human) Resume(ctx context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, err := decodeHuman(in.Node)
	if err != nil {
		return engine.NodeResult{}, err
	}
	s := h.svc
	round, delays, wiID := h.state(in)
	ev := in.Event
	ignored := engine.NodeResult{Suspend: &engine.Suspend{Ignored: true}}

	switch {
	case ev.Kind == "timer" && ev.TimerKey == "deadline":
		// 节点定时: void the round's work item and take the timeout exit
		if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_work_item SET status='voided', done_at=? WHERE run_id=? AND node_key=? AND status='active'", s.nowMs(), in.Run.ID, in.Node.Key); err != nil {
			return engine.NodeResult{}, err
		}
		if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_task SET status=?, updated_at=? WHERE id=?", StatusOpen, s.nowMs(), in.Run.TaskID); err != nil {
			return engine.NodeResult{}, err
		}
		s.log(ctx, in.Q, in.Run.TaskID, "timeout", "system", "「%s」到达节点定时仍未处理，本轮待办作废", in.Node.Name)
		return engine.NodeResult{Exit: deadlineExit(c)}, nil

	case ev.Kind == "timer" && strings.HasPrefix(ev.TimerKey, "remind"):
		return h.remind(ctx, in, c, round, delays)

	case ev.Kind == "action":
		p := expr.Vars(ev.Payload)
		if int64(toInt(p["work_item_id"])) != wiID {
			return ignored, nil
		}
		var wi WorkItem
		if err := in.Q.QueryRowCtx(ctx, &wi, "SELECT * FROM bw_work_item WHERE id=?", wiID); err != nil {
			return engine.NodeResult{}, err
		}
		if wi.Status != "active" {
			return ignored, nil
		}
		var btn *HumanButton
		for _, b := range visibleButtons(c, round, delays) {
			if b.Key == p.String("button") {
				b := b
				btn = &b
			}
		}
		if btn == nil {
			return ignored, nil
		}
		actor, note := p.String("actor"), p.String("note")
		now := s.nowMs()
		label := btn.Label
		if l := s.buttonLabel(decodeCard(wi.CardJSON), btn.Key); l != "" {
			label = l
		}
		switch btn.Kind {
		case "complete", "external":
			if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_work_item SET status='done', action=?, action_by=?, action_note=?, done_at=? WHERE id=?", btn.Key, actor, note, now, wiID); err != nil {
				return engine.NodeResult{}, err
			}
			if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_task SET status=?, updated_at=? WHERE id=?", StatusOpen, now, in.Run.TaskID); err != nil {
				return engine.NodeResult{}, err
			}
			s.log(ctx, in.Q, in.Run.TaskID, "action", actor, "%s 点击「%s」· 第 %d 轮完成", s.memberName(ctx, in.Q, actor), label, round)
			return engine.NodeResult{Exit: btn.Exit, Output: map[string]any{"node." + in.Node.Key + ".action": btn.Key}}, nil

		case "delay":
			if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_work_item SET status='delayed', action=?, action_by=?, action_note=?, done_at=? WHERE id=?", btn.Key, actor, note, now, wiID); err != nil {
				return engine.NodeResult{}, err
			}
			delays++
			due, err := expr.TimeExpr(c.Delay.RemindAt).Eval(in.Vars, in.Now)
			if err != nil {
				return engine.NodeResult{}, err
			}
			if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_task SET delay_count=delay_count+1, updated_at=? WHERE id=?", now, in.Run.TaskID); err != nil {
				return engine.NodeResult{}, err
			}
			if !due.After(in.Now) {
				s.log(ctx, in.Q, in.Run.TaskID, "action", actor, "%s 点击「%s」· 提醒时间已过，立即复查", s.memberName(ctx, in.Q, actor), label)
				return h.remind(ctx, in, c, round, delays)
			}
			text := fmt.Sprintf("延迟后，%s 会复查 Case 并再次提醒", s.fmtTime(due.UnixMilli()))
			if _, err := in.Q.ExecCtx(ctx, "UPDATE bw_task SET status=?, next_action_at=?, next_action_text=?, updated_at=? WHERE id=?",
				StatusOpen, due.UnixMilli(), text, now, in.Run.TaskID); err != nil {
				return engine.NodeResult{}, err
			}
			s.log(ctx, in.Q, in.Run.TaskID, "action", actor, "%s 点击「%s」· %s", s.memberName(ctx, in.Q, actor), label, text)
			if err := s.syncCard(ctx, in.Q, in.Run.TaskID); err != nil {
				return engine.NodeResult{}, err
			}
			return engine.NodeResult{
				State:   map[string]any{"round": round, "delays": delays, "work_item_id": wiID},
				Suspend: &engine.Suspend{Reason: "delayed", Timers: []engine.TimerSpec{{Key: fmt.Sprintf("remind:r%d", round), DueAt: due}}},
			}, nil
		}
	}
	return ignored, nil
}

// remind re-checks eligibility after a delay and opens the next round on the
// same task, or ends the task as invalidated.
func (h *Human) remind(ctx context.Context, in *engine.NodeInput, c *HumanConfig, round, delays int64) (engine.NodeResult, error) {
	s := h.svc
	t, err := s.Task(ctx, in.Q, in.Run.TaskID)
	if err != nil {
		return engine.NodeResult{}, err
	}
	vars := in.Vars
	if c.Delay == nil || c.Delay.Revalidate {
		ok, live, err := s.Revalidate(ctx, in.Q, t, in.Vars)
		if err != nil {
			return engine.NodeResult{}, fmt.Errorf("复查读取失败: %w", err)
		}
		if !ok {
			s.log(ctx, in.Q, t.ID, "recheck", "system", "到期复查：资格不再满足（Case 已关闭或失效）")
			return engine.NodeResult{Terminate: map[string]any{"close_reason": CloseInvalidated}}, nil
		}
		s.log(ctx, in.Q, t.ID, "recheck", "system", "到期复查：仍需跟进，开始第 %d 轮", round+1)
		vars = live
	}
	in.Vars = vars
	res, err := h.newRound(ctx, in, c, round+1, delays)
	if err != nil {
		return res, err
	}
	// live values refreshed during revalidation become run vars
	out := map[string]any{}
	for k, v := range vars {
		out[k] = v
	}
	for k, v := range res.Output {
		out[k] = v
	}
	res.Output = out
	return res, nil
}

func toInt(v any) int {
	f, _ := expr.ToFloat(v)
	return int(f)
}

var _ = time.Second
