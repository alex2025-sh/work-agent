package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"bywork/internal/expr"
	"bywork/internal/task"
)

// turnCtx is everything a tool call may touch: one session, one task type,
// one user. Tools re-check permissions through the task service.
type turnCtx struct {
	svc     *Service
	session *Session
	viewer  task.Viewer
	emit    func(Event)
}

func obj(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if len(required) == 0 {
		req = []byte("[]")
	}
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s,"additionalProperties":false}`, props, req))
}

// toolDefs is the complete tool surface. There is deliberately no tool that
// saves a draft, accepts a proposal, publishes, or changes a task type.
func toolDefs() []ToolDef {
	def := func(name, desc string, params json.RawMessage) ToolDef {
		return ToolDef{Type: "function", Function: FunctionDef{Name: name, Description: desc, Parameters: params}}
	}
	return []ToolDef{
		def("get_context", "读取当前任务类型：去重定义、团队、采集配置草稿（spec + revision）、流程草稿（graph + cards + revision）、流程的输入字段契约、生效版本号。每轮开始先调用一次。", obj("")),
		def("list_node_types", "列出流程可用的节点类型及其配置 JSON Schema。新增或修改节点前调用。", obj("")),
		def("list_capabilities", "列出取数节点可用的 data.* 能力（参数与产出字段）。", obj("")),
		def("describe_table", "查看只读数据源中一张表的列名与类型（不返回数据）。只能查看白名单中的表。",
			obj(`"datasource":{"type":"string"},"table":{"type":"string"}`, "datasource", "table")),
		def("sample_query", "在只读数据源上试运行一条 SELECT（最多 10 行；敏感列已脱敏）。变量写成 {{scan_from}} / {{now}}，只能出现在值的位置。",
			obj(`"datasource":{"type":"string"},"query":{"type":"string"}`, "datasource", "query")),
		def("propose_config", "提交采集配置（TaskConfig spec）的完整新版本作为修改提议。后端会做配置校验、只读试跑和字段契约检查并返回结果；有错误时请修正后重新提交。提议不会生效，需要用户在界面上接受。",
			obj(`"summary":{"type":"string","description":"一句话说明改了什么"},"spec":{"type":"object","description":"完整的 ConfigSpec，结构与 get_context 返回的 config.spec 相同"}`, "summary", "spec")),
		def("propose_flow", "以操作列表提交处理流程（含卡片）的修改提议。op 取值：add_node{key,type,name,config,after?}、update_node{key,name?,config?（整体替换该节点 config）}、remove_node{key}、connect{from,exit,to}、disconnect{from,exit}、set_card{key,template}、remove_card{key}。后端应用到草稿副本并做流程校验与字段契约检查；有错误请修正后重新提交。提议不会生效，需要用户在界面上接受。",
			obj(`"summary":{"type":"string"},"ops":{"type":"array","items":{"type":"object"}}`, "summary", "ops")),
	}
}

// run dispatches one tool call; the returned string goes back to the model.
func (t *turnCtx) run(ctx context.Context, name, args string) (string, error) {
	var a map[string]json.RawMessage
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("参数不是合法 JSON: %v", err)
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(a[k], &s)
		return s
	}
	var out any
	var err error
	switch name {
	case "get_context":
		out, err = t.getContext(ctx)
	case "list_node_types":
		var types []map[string]any
		for _, m := range t.svc.tasks.Engine().NodeTypes() {
			if m.Category != "phase1" {
				continue
			}
			types = append(types, map[string]any{"type": m.Type, "title": m.Title, "desc": m.Desc, "schema": m.Schema})
		}
		out = types
	case "list_capabilities":
		out = t.svc.tasks.Caps().List()
	case "describe_table":
		out, err = t.describeTable(ctx, str("datasource"), str("table"))
	case "sample_query":
		out, err = t.sampleQuery(ctx, str("datasource"), str("query"))
	case "propose_config":
		var spec task.ConfigSpec
		if e := json.Unmarshal(a["spec"], &spec); e != nil {
			return "", fmt.Errorf("spec 格式错误: %v", e)
		}
		out, err = t.proposeConfig(ctx, str("summary"), &spec)
	case "propose_flow":
		var ops []FlowOp
		if e := json.Unmarshal(a["ops"], &ops); e != nil {
			return "", fmt.Errorf("ops 格式错误: %v", e)
		}
		out, err = t.proposeFlow(ctx, str("summary"), ops)
	default:
		return "", fmt.Errorf("没有工具 %q", name)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func (t *turnCtx) getContext(ctx context.Context) (any, error) {
	key := t.session.TypeKey
	tt, err := t.svc.tasks.Type(ctx, t.svc.conn, key)
	if err != nil {
		return nil, err
	}
	cfg, err := t.svc.tasks.ConfigDraft(ctx, t.viewer.UserID, key)
	if err != nil {
		return nil, err
	}
	flow, err := t.svc.tasks.FlowDraft(ctx, t.viewer.UserID, key)
	if err != nil {
		return nil, err
	}
	v := t.svc.tasks.FlowCheck(flow)
	return map[string]any{
		"task_type": map[string]any{
			"key": tt.TypeKey, "name": tt.Name, "team": tt.Team, "object_type": tt.ObjectType, "status": tt.Status,
			"dedupe_fields": tt.DedupeFields(), "round_field": tt.RoundField,
			"active_config_version": tt.ActiveConfigVersion, "active_flow_version": tt.ActiveFlowVersion,
			"note": "去重定义创建后不可修改",
		},
		"config":         map[string]any{"revision": cfg.Revision, "spec": cfg.Spec},
		"flow":           map[string]any{"revision": flow.Revision, "graph": flow.Graph, "cards": flow.Cards},
		"flow_inputs":    v.Inputs,
		"flow_issues":    v.Issues,
		"datasources":    t.svc.tasks.Sources().Names(),
		"allowed_tables": t.svc.cfg.AllowedTables,
		"time_units":     "时长写法：48h、3d、30m；时间表达式：case.created_at + 3d",
	}, nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func (t *turnCtx) describeTable(ctx context.Context, source, table string) (any, error) {
	if !identRe.MatchString(table) || !t.svc.tableAllowed(table) {
		return nil, fmt.Errorf("表 %q 不在允许查看的白名单中（%s）", table, strings.Join(t.svc.cfg.AllowedTables, ", "))
	}
	res, err := t.svc.tasks.Sources().Query(ctx, source, "SELECT * FROM "+table+" LIMIT 1", expr.Vars{}, 1)
	if err != nil {
		return nil, err
	}
	cols := []map[string]string{}
	for _, c := range res.Columns {
		typ := "unknown"
		if len(res.Rows) > 0 {
			typ = valueType(res.Rows[0][c])
		}
		cols = append(cols, map[string]string{"name": c, "type": typ, "sensitive": fmt.Sprint(t.svc.sensitiveColumn(c))})
	}
	return map[string]any{"table": table, "columns": cols, "note": "时间列多为毫秒时间戳（BIGINT）"}, nil
}

func valueType(v any) string {
	switch v.(type) {
	case int64, int32, int, float64:
		return "number"
	case string:
		return "string"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func (t *turnCtx) sampleQuery(ctx context.Context, source, query string) (any, error) {
	if !t.svc.queryTablesAllowed(query) {
		return nil, fmt.Errorf("查询引用了白名单以外的表（允许：%s）", strings.Join(t.svc.cfg.AllowedTables, ", "))
	}
	sc, _ := t.svc.tasks.ScheduleInfo(ctx, t.session.TypeKey)
	var scanFrom int64
	if sc != nil {
		if row, ok := sc["schedule"].(task.ScheduleRow); ok {
			scanFrom = row.Watermark
		}
	}
	res, err := t.svc.tasks.Sources().Query(ctx, source, query, expr.Vars{"scan_from": scanFrom, "now": t.svc.now().UnixMilli(), "last_scan_at": int64(0)}, 10)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	out := map[string]any{"ok": true, "columns": res.Columns, "row_count": len(res.Rows), "truncated": res.Truncated}
	if t.svc.cfg.SampleRows != "none" {
		rows := make([]map[string]any, 0, len(res.Rows))
		for _, r := range res.Rows {
			rows = append(rows, t.svc.maskRow(r))
		}
		out["rows"] = rows
	}
	return out, nil
}

// ---------- proposals ----------

type checkResult struct {
	OK       bool     `json:"ok"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
	Info     []string `json:"info"`
}

func (c *checkResult) finish() {
	c.OK = len(c.Errors) == 0
	if c.Errors == nil {
		c.Errors = []string{}
	}
	if c.Warnings == nil {
		c.Warnings = []string{}
	}
	if c.Info == nil {
		c.Info = []string{}
	}
}

func (t *turnCtx) proposeConfig(ctx context.Context, summary string, spec *task.ConfigSpec) (any, error) {
	key := t.session.TypeKey
	tt, err := t.svc.tasks.Type(ctx, t.svc.conn, key)
	if err != nil {
		return nil, err
	}
	draft, err := t.svc.tasks.ConfigDraft(ctx, t.viewer.UserID, key)
	if err != nil {
		return nil, err
	}
	var res checkResult
	res.Errors = append(res.Errors, t.svc.tasks.ConfigCheck(tt, spec)...)
	if spec.Trigger.Has("sql") && !t.svc.queryTablesAllowed(spec.Trigger.Query) {
		res.Errors = append(res.Errors, "SQL 引用了白名单以外的表")
	}
	if len(res.Errors) == 0 {
		var scanFrom int64
		if sc, _ := t.svc.tasks.ScheduleInfo(ctx, key); sc != nil {
			if row, ok := sc["schedule"].(task.ScheduleRow); ok {
				scanFrom = row.Watermark
			}
		}
		dr := t.svc.tasks.DryRunConfig(ctx, tt, spec, scanFrom)
		res.Errors = append(res.Errors, dr.Errors...)
		if dr.OK && spec.Trigger.Has("sql") {
			res.Info = append(res.Info, fmt.Sprintf("试跑：扫描 %d 行，满足资格 %d，预计新建 %d 个任务，通知 %d 位 RM", dr.Rows, dr.Eligible, dr.NewTasks, dr.NotifyRMs))
		}
		res.Info = append(res.Info, dr.Checks...)
	}
	// field contract against both the flow draft and the active flow
	produced := map[string]bool{}
	for _, f := range spec.Produced() {
		produced[f] = true
	}
	if flow, err := t.svc.tasks.FlowDraft(ctx, t.viewer.UserID, key); err == nil {
		for _, need := range t.svc.tasks.FlowCheck(flow).Inputs {
			if !produced[need] {
				res.Errors = append(res.Errors, "字段契约：处理流程需要 "+need+"，这份配置不产出")
			}
		}
	}
	if old := draft.Spec.Produced(); len(old) > 0 {
		var removed []string
		for _, f := range old {
			if !produced[f] {
				removed = append(removed, f)
			}
		}
		if len(removed) > 0 {
			res.Warnings = append(res.Warnings, "移除了产出字段："+strings.Join(removed, "、"))
		}
	}
	res.finish()
	before := draft.Spec
	p, err := t.svc.saveProposal(ctx, t.session, t.viewer.UserID, "config", summary, draft.Revision,
		map[string]any{"before": before, "after": spec}, res)
	if err != nil {
		return nil, err
	}
	t.emit(Event{Type: "proposal", Proposal: p})
	return map[string]any{"proposal_id": p.ID, "status": p.Status, "validation": res,
		"next": nextHint(res.OK)}, nil
}

func (t *turnCtx) proposeFlow(ctx context.Context, summary string, ops []FlowOp) (any, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("ops 不能为空")
	}
	key := t.session.TypeKey
	draft, err := t.svc.tasks.FlowDraft(ctx, t.viewer.UserID, key)
	if err != nil {
		return nil, err
	}
	before, err := docFromDefinition(draft)
	if err != nil {
		return nil, err
	}
	after, _ := docFromDefinition(draft)
	types := map[string]bool{}
	for _, m := range t.svc.tasks.Engine().NodeTypes() {
		if m.Category == "phase1" {
			types[m.Type] = true
		}
	}
	var res checkResult
	if err := after.Apply(ops, types); err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.finish()
		return map[string]any{"validation": res, "next": "操作没有应用成功，修正后重新提交 propose_flow"}, nil
	}
	change := diffFlow(before, after)
	if change.Empty() {
		res.Errors = append(res.Errors, "这些操作没有改变任何内容")
	}
	v := t.svc.tasks.FlowCheck(after.Definition())
	for _, i := range v.Issues {
		msg := i.Message
		if i.Node != "" {
			msg = "「" + i.Node + "」" + msg
		}
		switch i.Level {
		case "error":
			res.Errors = append(res.Errors, msg)
		case "warning":
			res.Warnings = append(res.Warnings, msg)
		default:
			res.Info = append(res.Info, msg)
		}
	}
	// field contract against the config draft
	if cfg, err := t.svc.tasks.ConfigDraft(ctx, t.viewer.UserID, key); err == nil {
		produced := map[string]bool{}
		for _, f := range cfg.Spec.Produced() {
			produced[f] = true
		}
		var missing []string
		for _, need := range v.Inputs {
			if !produced[need] {
				missing = append(missing, need)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			res.Errors = append(res.Errors, "字段契约：流程需要 "+strings.Join(missing, "、")+"，采集配置草稿不产出（先用 propose_config 增加字段映射）")
		}
	}
	res.finish()
	p, err := t.svc.saveProposal(ctx, t.session, t.viewer.UserID, "flow", summary, draft.Revision,
		map[string]any{"ops": ops, "change": change, "before": before, "after": after}, res)
	if err != nil {
		return nil, err
	}
	t.emit(Event{Type: "proposal", Proposal: p})
	return map[string]any{"proposal_id": p.ID, "status": p.Status, "change": change, "validation": res, "next": nextHint(res.OK)}, nil
}

func nextHint(ok bool) string {
	if ok {
		return "校验通过。用一两句话告诉用户改了什么、在哪里查看 diff 并接受；不要声称已经生效。"
	}
	return "校验未通过。根据 errors 修正后重新提交；无法修正时向用户说明原因。"
}
