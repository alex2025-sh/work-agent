// Package nodes contains the generic phase-1 node executors: start, end,
// wait, condition and data. The human node lives in the task domain.
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"bywork/internal/datasrc"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

func decode[T any](n *engine.Node) (T, error) {
	var c T
	if len(n.Config) == 0 || string(n.Config) == "null" {
		return c, nil
	}
	err := json.Unmarshal(n.Config, &c)
	return c, err
}

func cfgErr(n *engine.Node, err error) []engine.FieldError {
	return []engine.FieldError{{Node: n.Key, Level: "error", Message: "配置格式错误: " + err.Error()}}
}

// ---------- start ----------

type Start struct{}

func (Start) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "start", Title: "开始", Category: "phase1", Desc: "接收任务数据", Color: "#6b7280",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`)}
}
func (Start) ValidateConfig(*engine.Node, *engine.ValidateCtx) []engine.FieldError { return nil }
func (Start) Exits(*engine.Node) []string                                          { return []string{"next"} }
func (Start) Fields(*engine.Node) ([]string, []string)                             { return nil, nil }
func (Start) Execute(context.Context, *engine.NodeInput) (engine.NodeResult, error) {
	return engine.NodeResult{Exit: "next"}, nil
}

// ---------- end ----------

type EndConfig struct {
	CloseReason string `json:"close_reason"`
}

type End struct{}

var CloseReasons = []string{"completed", "invalidated", "admin_terminated", "flow_failed"}

func (End) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "end", Title: "结束", Category: "phase1", Desc: "关闭任务", Color: "#374151",
		Schema: json.RawMessage(`{"type":"object","properties":{"close_reason":{"type":"string","title":"关闭原因","enum":["completed","invalidated","admin_terminated","flow_failed"],"enumNames":["处理完成","Case 已失效","管理员终止","流程执行失败"],"default":"completed"}}}`)}
}
func (End) ValidateConfig(n *engine.Node, _ *engine.ValidateCtx) []engine.FieldError {
	c, err := decode[EndConfig](n)
	if err != nil {
		return cfgErr(n, err)
	}
	for _, r := range CloseReasons {
		if r == c.CloseReason || c.CloseReason == "" {
			return nil
		}
	}
	return []engine.FieldError{{Field: "close_reason", Message: "未知关闭原因 " + c.CloseReason}}
}
func (End) Exits(*engine.Node) []string              { return nil }
func (End) Fields(*engine.Node) ([]string, []string) { return nil, nil }
func (End) Execute(_ context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, _ := decode[EndConfig](in.Node)
	if c.CloseReason == "" {
		c.CloseReason = "completed"
	}
	return engine.NodeResult{Terminate: map[string]any{"close_reason": c.CloseReason, "end_node": in.Node.Key}}, nil
}

// ---------- wait ----------

type WaitConfig struct {
	Until string `json:"until"` // time expression, e.g. case.stage_entered_at + 7d
}

type Wait struct{}

func (Wait) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "wait", Title: "等待", Category: "phase1", Desc: "挂起到某个时间点", Color: "#b45309",
		Schema: json.RawMessage(`{"type":"object","required":["until"],"properties":{"until":{"type":"string","title":"等到","description":"基准字段 + 时长，如 case.stage_entered_at + 7d；或固定时间 RFC3339"}}}`)}
}
func (Wait) ValidateConfig(n *engine.Node, _ *engine.ValidateCtx) []engine.FieldError {
	c, err := decode[WaitConfig](n)
	if err != nil {
		return cfgErr(n, err)
	}
	if err := expr.TimeExpr(c.Until).Validate(); err != nil || c.Until == "" {
		return []engine.FieldError{{Field: "until", Message: "等待时间无效: " + fmt.Sprint(err)}}
	}
	return nil
}
func (Wait) Exits(*engine.Node) []string { return []string{"next"} }
func (Wait) Fields(n *engine.Node) ([]string, []string) {
	c, _ := decode[WaitConfig](n)
	return expr.TimeExpr(c.Until).Fields(), nil
}
func (Wait) Execute(_ context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, _ := decode[WaitConfig](in.Node)
	due, err := expr.TimeExpr(c.Until).Eval(in.Vars, in.Now)
	if err != nil {
		return engine.NodeResult{}, err
	}
	if !due.After(in.Now) {
		return engine.NodeResult{Exit: "next"}, nil
	}
	return engine.NodeResult{Suspend: &engine.Suspend{Reason: "wait until " + due.Format(time.RFC3339), Timers: []engine.TimerSpec{{Key: "wait", DueAt: due}}}}, nil
}
func (Wait) Resume(_ context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	if in.Event.Kind == "timer" && in.Event.TimerKey == "wait" {
		return engine.NodeResult{Exit: "next"}, nil
	}
	return engine.NodeResult{Suspend: &engine.Suspend{Ignored: true}}, nil
}

// ---------- condition ----------

type Branch struct {
	Exit  string     `json:"exit"`
	Label string     `json:"label,omitempty"`
	When  expr.Group `json:"when"`
}

type ConditionConfig struct {
	Branches    []Branch `json:"branches"`
	DefaultExit string   `json:"default_exit"`
}

type Condition struct{}

func (Condition) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "condition", Title: "条件分支", Category: "phase1", Desc: "按规则分支：多个出口 + 默认出口", Color: "#7c3aed",
		Schema: json.RawMessage(`{"type":"object","properties":{
 "branches":{"type":"array","title":"分支（自上而下第一个命中的生效）","items":{"type":"object","properties":{
   "exit":{"type":"string","title":"出口名"},"label":{"type":"string","title":"说明"},
   "when":{"type":"object","title":"条件","x-widget":"conditions"}}}},
 "default_exit":{"type":"string","title":"默认出口","default":"default"}}}`)}
}
func (Condition) ValidateConfig(n *engine.Node, _ *engine.ValidateCtx) []engine.FieldError {
	c, err := decode[ConditionConfig](n)
	if err != nil {
		return cfgErr(n, err)
	}
	var out []engine.FieldError
	if c.DefaultExit == "" {
		out = append(out, engine.FieldError{Field: "default_exit", Message: "条件节点必须有默认出口"})
	}
	seen := map[string]bool{c.DefaultExit: true}
	for _, b := range c.Branches {
		if b.Exit == "" {
			out = append(out, engine.FieldError{Field: "branches", Message: "分支缺少出口名"})
		}
		if seen[b.Exit] {
			out = append(out, engine.FieldError{Field: "branches", Message: "出口名重复: " + b.Exit})
		}
		seen[b.Exit] = true
		if b.When.Empty() {
			out = append(out, engine.FieldError{Field: "branches", Message: "分支 " + b.Exit + " 没有条件"})
		}
		for _, m := range b.When.Validate() {
			out = append(out, engine.FieldError{Field: "branches", Message: m})
		}
	}
	return out
}
func (Condition) Exits(n *engine.Node) []string {
	c, _ := decode[ConditionConfig](n)
	var out []string
	for _, b := range c.Branches {
		out = append(out, b.Exit)
	}
	if c.DefaultExit != "" {
		out = append(out, c.DefaultExit)
	}
	return out
}
func (Condition) Fields(n *engine.Node) ([]string, []string) {
	c, _ := decode[ConditionConfig](n)
	var reads []string
	for _, b := range c.Branches {
		reads = append(reads, b.When.Fields()...)
	}
	return reads, nil
}
func (Condition) Execute(_ context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, _ := decode[ConditionConfig](in.Node)
	for _, b := range c.Branches {
		ok, matched, err := b.When.Eval(in.Vars, in.Now)
		if err != nil {
			return engine.NodeResult{}, err
		}
		if ok {
			return engine.NodeResult{Exit: b.Exit, Output: map[string]any{"node." + in.Node.Key + ".matched": matched}}, nil
		}
	}
	return engine.NodeResult{Exit: c.DefaultExit}, nil
}

// ---------- data (取数) ----------

type DataConfig struct {
	Capability string            `json:"capability"`
	Args       map[string]string `json:"args"`     // values are templates, e.g. {{case.appeal_id}}
	OnError    string            `json:"on_error"` // suspend | retry
	Retries    int               `json:"retries"`
	Backoff    string            `json:"backoff"` // e.g. 5m
}

type Data struct{ Caps *datasrc.Registry }

func (d Data) Meta() engine.NodeMeta {
	return engine.NodeMeta{Type: "data", Title: "取数 / 刷新事实", Category: "phase1", Desc: "调用 data.* 实时读取业务数据，受数据权限管控", Color: "#0f766e",
		Schema: json.RawMessage(`{"type":"object","required":["capability"],"properties":{
 "capability":{"type":"string","title":"能力","x-widget":"capability"},
 "args":{"type":"object","title":"参数（可用 {{字段}}）","additionalProperties":{"type":"string"}},
 "on_error":{"type":"string","title":"失败策略","enum":["suspend","retry"],"enumNames":["挂起并告警","重试后挂起"],"default":"suspend"},
 "retries":{"type":"integer","title":"重试次数","default":3},
 "backoff":{"type":"string","title":"重试间隔","default":"5m"}}}`)}
}
func (d Data) ValidateConfig(n *engine.Node, vc *engine.ValidateCtx) []engine.FieldError {
	c, err := decode[DataConfig](n)
	if err != nil {
		return cfgErr(n, err)
	}
	cp, ok := d.Caps.Get(c.Capability)
	if !ok {
		return []engine.FieldError{{Field: "capability", Message: "未知能力 " + c.Capability}}
	}
	var out []engine.FieldError
	for _, a := range cp.Args {
		if c.Args[a] == "" {
			out = append(out, engine.FieldError{Field: "args." + a, Message: "缺少参数 " + a})
		}
	}
	if c.OnError == "retry" && c.Backoff != "" {
		if _, err := expr.ParseDuration(c.Backoff); err != nil {
			out = append(out, engine.FieldError{Field: "backoff", Message: err.Error()})
		}
	}
	return out
}
func (Data) Exits(*engine.Node) []string { return []string{"next"} }
func (d Data) Fields(n *engine.Node) ([]string, []string) {
	c, _ := decode[DataConfig](n)
	var reads []string
	for _, v := range c.Args {
		reads = append(reads, expr.TemplateFields(v)...)
	}
	var writes []string
	if cp, ok := d.Caps.Get(c.Capability); ok {
		writes = cp.Outputs
	}
	return reads, writes
}
func (d Data) Execute(ctx context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	c, _ := decode[DataConfig](in.Node)
	cp, ok := d.Caps.Get(c.Capability)
	if !ok {
		return engine.NodeResult{}, fmt.Errorf("unknown capability %s", c.Capability)
	}
	args := map[string]string{}
	for k, v := range c.Args {
		args[k] = expr.Render(v, in.Vars)
	}
	out, err := cp.Call(ctx, in.Q, args)
	if err == nil {
		out["node."+in.Node.Key+".read_at"] = in.Now.Format(time.RFC3339)
		return engine.NodeResult{Exit: "next", Output: out}, nil
	}
	if c.OnError == "retry" {
		attempt := 0
		if f, ok := expr.ToFloat(in.State["attempt"]); ok {
			attempt = int(f)
		}
		max := c.Retries
		if max <= 0 {
			max = 3
		}
		if attempt < max {
			backoff, _ := expr.ParseDuration(c.Backoff)
			if backoff <= 0 {
				backoff = 5 * time.Minute
			}
			return engine.NodeResult{
				State:   map[string]any{"attempt": attempt + 1, "last_error": err.Error()},
				Suspend: &engine.Suspend{Reason: "retry after error: " + err.Error(), Timers: []engine.TimerSpec{{Key: fmt.Sprintf("retry:%d", attempt+1), DueAt: in.Now.Add(backoff)}}},
			}, nil
		}
	}
	return engine.NodeResult{}, fmt.Errorf("取数 %s 失败: %w", c.Capability, err)
}
func (d Data) Resume(ctx context.Context, in *engine.NodeInput) (engine.NodeResult, error) {
	if in.Event.Kind != "timer" {
		return engine.NodeResult{Suspend: &engine.Suspend{Ignored: true}}, nil
	}
	return d.Execute(ctx, in)
}
