package task

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// ---------- task types ----------

type CreateTypeReq struct {
	TypeKey      string   `json:"type_key"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Team         string   `json:"team"`
	ObjectType   string   `json:"object_type"`
	DedupeFields []string `json:"dedupe_fields"`
	RoundField   string   `json:"round_field"`
}

var typeKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

// CreateType creates an immutable TaskType with empty drafts on both lines.
func (s *Service) CreateType(ctx context.Context, actor string, r CreateTypeReq) (*TaskType, error) {
	if !typeKeyRe.MatchString(r.TypeKey) {
		return nil, BadRequest("type_key 需为大写字母、数字、下划线，如 VIP_CASE_CREATED")
	}
	if r.Name == "" || r.Team == "" || len(r.DedupeFields) == 0 {
		return nil, BadRequest("名称、负责团队、去重字段必填")
	}
	err := s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
		now := s.nowMs()
		if _, err := q.ExecCtx(ctx, `INSERT INTO bw_task_type (type_key, name, description, team, visibility, object_type, dedupe_fields, round_field, status, creator, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.TypeKey, r.Name, r.Description, r.Team, "team", r.ObjectType, db.JSON(r.DedupeFields), r.RoundField, TypeDraft, actor, now, now); err != nil {
			if db.IsDuplicate(err) {
				return Conflict(nil, "任务类型 %s 已存在", r.TypeKey)
			}
			return err
		}
		spec := ConfigSpec{Trigger: Trigger{Sources: []string{"sql"}, Datasource: "demo"}, MaxPerRun: 200,
			Schedule: Schedule{Cron: "*/5 * * * *", Timezone: "UTC", Enabled: true}}
		for _, f := range append(r.DedupeFields, nonEmpty(r.RoundField)...) {
			col := f[strings.LastIndex(f, ".")+1:]
			spec.Fields = append(spec.Fields, FieldMap{Column: col, Field: f, Type: "string", Required: true, Sensitivity: "normal"})
		}
		if _, err := q.ExecCtx(ctx, "INSERT INTO bw_task_config (type_key, version, status, spec, author, created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
			r.TypeKey, 0, "draft", db.JSON(spec), actor, now, now); err != nil {
			return err
		}
		g := engine.Graph{Nodes: []engine.Node{{Key: "start", Type: "start", Name: "开始"}, {Key: "end", Type: "end", Name: "结束", Config: json.RawMessage(`{"close_reason":"completed"}`)}},
			Edges: []engine.Edge{{From: "start", Exit: "next", To: "end"}}}
		if _, err := q.ExecCtx(ctx, "INSERT INTO wf_definition (flow_key, version, status, graph, layout, cards, author, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
			r.TypeKey, 0, "draft", db.JSON(g), `{"start":{"x":0,"y":0},"end":{"x":0,"y":160}}`, "{}", actor, now, now); err != nil {
			return err
		}
		_, err := q.ExecCtx(ctx, "INSERT INTO bw_schedule (type_key, cron, timezone, status, updated_at) VALUES (?,?,?,?,?)", r.TypeKey, spec.Schedule.Cron, spec.Schedule.Timezone, "paused", now)
		s.Audit(ctx, q, actor, "create_type", r.TypeKey, r)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Type(ctx, s.conn, r.TypeKey)
}

func (s *Service) ListTypes(ctx context.Context) ([]TaskType, error) {
	out := []TaskType{}
	err := s.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM bw_task_type ORDER BY id")
	return out, err
}

// ---------- drafts ----------

// ConfigDraft returns the editable draft, creating one from the active
// version when none exists.
func (s *Service) ConfigDraft(ctx context.Context, actor, key string) (*ConfigRow, error) {
	if c, err := s.Config(ctx, s.conn, key, 0); err == nil {
		return c, nil
	}
	tt, err := s.Type(ctx, s.conn, key)
	if err != nil {
		return nil, err
	}
	base, err := s.Config(ctx, s.conn, key, tt.ActiveConfigVersion)
	if err != nil {
		return nil, err
	}
	now := s.nowMs()
	if _, err := s.conn.ExecCtx(ctx, "INSERT INTO bw_task_config (type_key, version, status, base_version, revision, spec, author, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
		key, 0, "draft", base.Version, base.Revision, base.SpecJSON, actor, now, now); err != nil && !db.IsDuplicate(err) {
		return nil, err
	}
	return s.Config(ctx, s.conn, key, 0)
}

func (s *Service) SaveConfigDraft(ctx context.Context, actor, key string, spec ConfigSpec, note string) (*ConfigRow, error) {
	if _, err := s.ConfigDraft(ctx, actor, key); err != nil {
		return nil, err
	}
	if _, err := s.conn.ExecCtx(ctx, "UPDATE bw_task_config SET spec=?, note=?, revision=revision+1, author=?, updated_at=? WHERE type_key=? AND version=0",
		db.JSON(spec), note, actor, s.nowMs(), key); err != nil {
		return nil, err
	}
	return s.Config(ctx, s.conn, key, 0)
}

func (s *Service) FlowDraft(ctx context.Context, actor, key string) (*engine.Definition, error) {
	if d, err := engine.LoadDefinition(ctx, s.conn, key, 0); err == nil {
		return d, nil
	}
	tt, err := s.Type(ctx, s.conn, key)
	if err != nil {
		return nil, err
	}
	base, err := engine.LoadDefinition(ctx, s.conn, key, tt.ActiveFlowVersion)
	if err != nil {
		return nil, err
	}
	now := s.nowMs()
	if _, err := s.conn.ExecCtx(ctx, "INSERT INTO wf_definition (flow_key, version, status, base_version, revision, graph, layout, cards, author, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		key, 0, "draft", base.Version, base.Revision, base.GraphJSON, base.LayoutJSON, base.CardsJSON, actor, now, now); err != nil && !db.IsDuplicate(err) {
		return nil, err
	}
	return engine.LoadDefinition(ctx, s.conn, key, 0)
}

type FlowDraftReq struct {
	Graph  engine.Graph    `json:"graph"`
	Layout json.RawMessage `json:"layout"`
	Cards  json.RawMessage `json:"cards"`
	Note   string          `json:"note"`
}

func (s *Service) SaveFlowDraft(ctx context.Context, actor, key string, r FlowDraftReq) (*engine.Definition, error) {
	if _, err := s.FlowDraft(ctx, actor, key); err != nil {
		return nil, err
	}
	layout, cards := string(r.Layout), string(r.Cards)
	if layout == "" {
		layout = "{}"
	}
	if cards == "" {
		cards = "{}"
	}
	var probe map[string]CardTemplate
	if err := json.Unmarshal([]byte(cards), &probe); err != nil {
		return nil, BadRequest("卡片格式错误: %v", err)
	}
	if _, err := s.conn.ExecCtx(ctx, "UPDATE wf_definition SET graph=?, layout=?, cards=?, note=?, revision=revision+1, author=?, updated_at=? WHERE flow_key=? AND version=0",
		db.JSON(r.Graph), layout, cards, r.Note, actor, s.nowMs(), key); err != nil {
		return nil, err
	}
	return engine.LoadDefinition(ctx, s.conn, key, 0)
}

// ---------- validation & contract ----------

// FlowCheck validates a flow and computes its input contract, including the
// fields its card templates display.
func (s *Service) FlowCheck(def *engine.Definition) *engine.Validation {
	cards := cardTemplates(def)
	caps := map[string][]string{}
	for _, c := range s.caps.List() {
		caps[c.Name] = c.Outputs
	}
	v := s.eng.Validate(&def.Graph, &engine.ValidateCtx{Capabilities: caps, Extra: map[string]any{"cards": cards}})
	// fields shown on cards: required unless produced by a data node
	written := map[string]bool{}
	for i := range def.Graph.Nodes {
		n := &def.Graph.Nodes[i]
		if x, ok := s.eng.Executor(n.Type); ok {
			_, w := x.Fields(n)
			for _, f := range w {
				written[f] = true
			}
		}
	}
	inputs := v.Inputs
	used := map[string]bool{}
	for i := range def.Graph.Nodes {
		n := &def.Graph.Nodes[i]
		if n.Type != "human" {
			continue
		}
		hc, _ := decodeHuman(n)
		used[hc.Card] = true
		tpl, ok := cards[hc.Card]
		if !ok {
			continue
		}
		for _, f := range tpl.Fields {
			if !written[f.Field] && !strings.HasPrefix(f.Field, "node.") {
				inputs = append(inputs, f.Field)
			}
		}
		for _, f := range expr.TemplateFields(tpl.Title) {
			if !written[f] && !strings.HasPrefix(f, "node.") {
				inputs = append(inputs, f)
			}
		}
	}
	v.Inputs = expr.Uniq(inputs)
	return v
}

// ConfigCheck validates a TaskConfig spec.
func (s *Service) ConfigCheck(tt *TaskType, spec *ConfigSpec) []string {
	errs := []string{}
	if len(spec.Trigger.Sources) == 0 {
		errs = append(errs, "至少需要一个触发来源")
	}
	for _, src := range spec.Trigger.Sources {
		switch src {
		case "sql":
			if spec.Trigger.Query == "" {
				errs = append(errs, "SQL 来源需要查询")
			} else if _, _, err := datasrc.Compile(spec.Trigger.Query, expr.Vars{}); err != nil {
				errs = append(errs, "SQL: "+err.Error())
			}
			found := false
			for _, n := range s.sources.Names() {
				if n == spec.Trigger.Datasource {
					found = true
				}
			}
			if !found {
				errs = append(errs, "未知数据源 "+spec.Trigger.Datasource)
			}
		case "push", "flow":
		case "mcp":
			if spec.Trigger.MCPTool == "" {
				errs = append(errs, "MCP 来源需要工具名")
			}
		default:
			errs = append(errs, "未知触发来源 "+src)
		}
	}
	produced := map[string]bool{}
	for _, f := range spec.Fields {
		if f.Column == "" || f.Field == "" {
			errs = append(errs, "字段映射存在空列或空字段")
		}
		if produced[f.Field] {
			errs = append(errs, "字段重复: "+f.Field)
		}
		produced[f.Field] = true
	}
	for _, f := range append(tt.DedupeFields(), nonEmpty(tt.RoundField)...) {
		if !produced[f] {
			errs = append(errs, "去重字段 "+f+" 不在字段映射中")
		}
	}
	errs = append(errs, spec.Eligibility.Validate()...)
	for _, r := range spec.LiveRefresh {
		if _, ok := s.caps.Get(r.Capability); !ok {
			errs = append(errs, "未知能力 "+r.Capability)
		}
	}
	if spec.Invalidation.RecheckEvery != "" {
		if _, err := expr.ParseDuration(spec.Invalidation.RecheckEvery); err != nil {
			errs = append(errs, "失效复查间隔: "+err.Error())
		}
	}
	if spec.Trigger.Has("sql") {
		if _, _, err := ValidateCron(spec.Schedule.Cron, spec.Schedule.Timezone); err != nil {
			errs = append(errs, "调度: "+err.Error())
		}
	}
	if spec.MaxPerRun <= 0 {
		errs = append(errs, "单次建任务上限必须大于 0")
	}
	return errs
}

func missingFields(produced, required []string) []string {
	have := map[string]bool{}
	for _, p := range produced {
		have[p] = true
	}
	out := []string{}
	for _, r := range required {
		if !have[r] {
			out = append(out, r)
		}
	}
	return out
}

// ---------- publish ----------

type Check struct {
	Level string `json:"level"` // pass | error | warning | info
	Text  string `json:"text"`
}

type ScopeOption struct {
	Scope     string `json:"scope"` // both | config | flow
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

type PublishPlan struct {
	TypeKey        string             `json:"type_key"`
	Scope          string             `json:"scope"`
	Options        []ScopeOption      `json:"options"`
	Checks         []Check            `json:"checks"`
	OK             bool               `json:"ok"`
	ConfigFrom     int64              `json:"config_from"` // draft revision
	ConfigTo       int64              `json:"config_to"`   // new version
	FlowFrom       int64              `json:"flow_from"`
	FlowTo         int64              `json:"flow_to"`
	ActiveConfig   int64              `json:"active_config"`
	ActiveFlow     int64              `json:"active_flow"`
	DryRun         *DryRun            `json:"dry_run,omitempty"`
	FlowValidation *engine.Validation `json:"flow_validation,omitempty"`
	Produced       []string           `json:"produced"`
	Required       []string           `json:"required"`
}

func (s *Service) maxVersion(ctx context.Context, table, keyCol, key string) int64 {
	var v int64
	_ = s.conn.QueryRowCtx(ctx, &v, fmt.Sprintf("SELECT COALESCE(MAX(version),0) FROM %s WHERE %s=?", table, keyCol), key)
	return v
}

// PlanPublish runs every pre-publish check for a scope without changing anything.
func (s *Service) PlanPublish(ctx context.Context, actor, key, scope string) (*PublishPlan, error) {
	tt, err := s.Type(ctx, s.conn, key)
	if err != nil {
		return nil, err
	}
	cfgDraft, err := s.ConfigDraft(ctx, actor, key)
	if err != nil {
		return nil, err
	}
	flowDraft, err := s.FlowDraft(ctx, actor, key)
	if err != nil {
		return nil, err
	}
	var activeCfg *ConfigRow
	var activeFlow *engine.Definition
	if tt.ActiveConfigVersion > 0 {
		activeCfg, _ = s.Config(ctx, s.conn, key, tt.ActiveConfigVersion)
	}
	if tt.ActiveFlowVersion > 0 {
		activeFlow, _ = engine.LoadDefinition(ctx, s.conn, key, tt.ActiveFlowVersion)
	}
	draftProduced := cfgDraft.Spec.Produced()
	draftRequired := s.FlowCheck(flowDraft).Inputs

	// scope availability
	opts := []ScopeOption{{Scope: "both", Available: true}}
	cfgOpt := ScopeOption{Scope: "config", Available: true}
	if activeFlow == nil {
		cfgOpt.Available, cfgOpt.Reason = false, "还没有已发布的流程"
	} else if miss := missingFields(draftProduced, s.FlowCheck(activeFlow).Inputs); len(miss) > 0 {
		cfgOpt.Available, cfgOpt.Reason = false, fmt.Sprintf("当前流程 v%d 需要 %s，草稿不产出", activeFlow.Version, strings.Join(miss, "、"))
	} else {
		cfgOpt.Reason = fmt.Sprintf("与当前流程 v%d 兼容", activeFlow.Version)
	}
	flowOpt := ScopeOption{Scope: "flow", Available: true}
	if activeCfg == nil {
		flowOpt.Available, flowOpt.Reason = false, "还没有已发布的采集配置"
	} else if miss := missingFields(activeCfg.Spec.Produced(), draftRequired); len(miss) > 0 {
		flowOpt.Available, flowOpt.Reason = false, fmt.Sprintf("不可用：流程需要 %s，当前配置 v%d 不产出", strings.Join(miss, "、"), activeCfg.Version)
	} else {
		flowOpt.Reason = fmt.Sprintf("与当前配置 v%d 兼容", activeCfg.Version)
	}
	opts = append(opts, cfgOpt, flowOpt)
	if scope == "" {
		scope = "both"
	}
	p := &PublishPlan{TypeKey: key, Scope: scope, Options: opts, ActiveConfig: tt.ActiveConfigVersion, ActiveFlow: tt.ActiveFlowVersion,
		ConfigFrom: cfgDraft.Revision, FlowFrom: flowDraft.Revision}
	add := func(level, f string, a ...any) {
		p.Checks = append(p.Checks, Check{Level: level, Text: fmt.Sprintf(f, a...)})
	}
	for _, o := range opts {
		if o.Scope == scope && !o.Available {
			add("error", "%s", o.Reason)
		}
	}

	spec, def := cfgDraft.Spec, flowDraft
	if scope == "flow" && activeCfg != nil {
		spec = activeCfg.Spec
	}
	if scope == "config" && activeFlow != nil {
		def = activeFlow
	}
	if scope != "flow" {
		p.ConfigTo = s.maxVersion(ctx, "bw_task_config", "type_key", key) + 1
		for _, e := range s.ConfigCheck(tt, spec) {
			add("error", "配置: %s", e)
		}
		var sc ScheduleRow
		_ = s.conn.QueryRowCtx(ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", key)
		p.DryRun = s.DryRunConfig(ctx, tt, spec, sc.Watermark)
		for _, e := range p.DryRun.Errors {
			add("error", "试跑: %s", e)
		}
		if p.DryRun.OK {
			add("pass", "通过 · 试跑：样本 %d 行，去重键唯一，只读权限通过", min(p.DryRun.Rows, 20))
		}
	}
	if scope != "config" {
		p.FlowTo = s.maxVersion(ctx, "wf_definition", "flow_key", key) + 1
	}
	fv := s.FlowCheck(def)
	p.FlowValidation = fv
	for _, i := range fv.Issues {
		switch i.Level {
		case "error":
			add("error", "流程「%s」: %s", i.Node, i.Message)
		case "warning":
			add("warning", "流程: %s", i.Message)
		}
	}
	if fv.OK() {
		add("pass", "通过 · 流程校验：节点可达，每个出口都有去向")
	}
	p.Produced, p.Required = spec.Produced(), fv.Inputs
	if miss := missingFields(p.Produced, p.Required); len(miss) > 0 {
		add("error", "字段契约：流程需要 %s，配置不产出", strings.Join(miss, "、"))
	} else {
		add("pass", "通过 · 字段契约：流程需要 %d 个字段，配置全部产出", len(p.Required))
	}
	add("info", "去重定义来自任务类型，本次发布不改变")
	p.OK = true
	for _, c := range p.Checks {
		if c.Level == "error" {
			p.OK = false
		}
	}
	return p, nil
}

type PublishReq struct {
	Scope    string `json:"scope"`
	Rescan   string `json:"rescan"`   // continue | duration
	Duration string `json:"duration"` // e.g. 7d (rescan=duration)
	Note     string `json:"note"`
}

// Publish atomically switches the chosen line(s) to new versions. In-flight
// tasks keep the versions they were created with.
func (s *Service) Publish(ctx context.Context, actor, key string, r PublishReq) (*PublishPlan, error) {
	p, err := s.PlanPublish(ctx, actor, key, r.Scope)
	if err != nil {
		return nil, err
	}
	if !p.OK {
		return p, Conflict(p, "发布前检查未通过")
	}
	err = s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
		now := s.nowMs()
		if p.ConfigTo > 0 {
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_config SET version=?, status='published', published_at=?, note=CASE WHEN ?<>'' THEN ? ELSE note END WHERE type_key=? AND version=0",
				p.ConfigTo, now, r.Note, r.Note, key); err != nil {
				return err
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_type SET active_config_version=?, updated_at=? WHERE type_key=?", p.ConfigTo, now, key); err != nil {
				return err
			}
			if err := s.applySchedule(ctx, q, key, r); err != nil {
				return err
			}
		}
		if p.FlowTo > 0 {
			if _, err := q.ExecCtx(ctx, "UPDATE wf_definition SET version=?, status='published', published_at=?, note=CASE WHEN ?<>'' THEN ? ELSE note END WHERE flow_key=? AND version=0",
				p.FlowTo, now, r.Note, r.Note, key); err != nil {
				return err
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_type SET active_flow_version=?, updated_at=? WHERE type_key=?", p.FlowTo, now, key); err != nil {
				return err
			}
		}
		s.Audit(ctx, q, actor, "publish", key, map[string]any{"scope": p.Scope, "config": p.ConfigTo, "flow": p.FlowTo, "rescan": r.Rescan, "duration": r.Duration})
		return nil
	})
	return p, err
}

// applySchedule switches the scan plan to the newly published config: the old
// plan is replaced; scanning continues from the current watermark or rescans
// the given duration.
func (s *Service) applySchedule(ctx context.Context, q db.Querier, key string, r PublishReq) error {
	tt, err := s.Type(ctx, q, key)
	if err != nil {
		return err
	}
	cfg, err := s.Config(ctx, q, key, tt.ActiveConfigVersion)
	if err != nil {
		return err
	}
	var sc ScheduleRow
	if err := q.QueryRowCtx(ctx, &sc, "SELECT * FROM bw_schedule WHERE type_key=?", key); err != nil {
		return err
	}
	watermark := sc.Watermark
	if r.Rescan == "duration" {
		d, err := expr.ParseDuration(r.Duration)
		if err != nil {
			return BadRequest("回扫时长: %v", err)
		}
		watermark = s.nowMs() - d.Milliseconds()
	}
	status := sc.Status
	if !cfg.Spec.Trigger.Has("sql") || !cfg.Spec.Schedule.Enabled {
		status = "paused"
	} else if tt.Status == TypeEnabled || tt.Status == TypeShadow {
		status = "active"
	}
	next := s.nowMs()
	if f := NextFires(cfg.Spec.Schedule.Cron, cfg.Spec.Schedule.Timezone, s.now(), 1); len(f) == 1 {
		next = clock.Ms(f[0])
	}
	_, err = q.ExecCtx(ctx, "UPDATE bw_schedule SET config_version=?, cron=?, timezone=?, status=?, pause_reason='', watermark=?, next_fire_at=?, checkpoint='', updated_at=? WHERE type_key=?",
		cfg.Version, cfg.Spec.Schedule.Cron, cfg.Spec.Schedule.Timezone, status, watermark, next, s.nowMs(), key)
	return err
}

// Rollback copies an old version into the draft and publishes it as a new
// version; history is never rewritten.
func (s *Service) Rollback(ctx context.Context, actor, key, kind string, version int64) (*PublishPlan, error) {
	switch kind {
	case "config":
		old, err := s.Config(ctx, s.conn, key, version)
		if err != nil {
			return nil, err
		}
		if _, err := s.SaveConfigDraft(ctx, actor, key, *old.Spec, fmt.Sprintf("回滚到 v%d", version)); err != nil {
			return nil, err
		}
	case "flow":
		old, err := engine.LoadDefinition(ctx, s.conn, key, version)
		if err != nil {
			return nil, err
		}
		if _, err := s.SaveFlowDraft(ctx, actor, key, FlowDraftReq{Graph: old.Graph, Layout: old.Layout, Cards: old.Cards, Note: fmt.Sprintf("回滚到 v%d", version)}); err != nil {
			return nil, err
		}
	default:
		return nil, BadRequest("kind 只能是 config 或 flow")
	}
	return s.Publish(ctx, actor, key, PublishReq{Scope: kind, Rescan: "continue", Note: fmt.Sprintf("回滚到 v%d", version)})
}

func (s *Service) ConfigVersions(ctx context.Context, key string) ([]ConfigRow, error) {
	out := []ConfigRow{}
	if err := s.conn.QueryRowsPartialCtx(ctx, &out, "SELECT * FROM bw_task_config WHERE type_key=? ORDER BY version DESC", key); err != nil {
		return nil, err
	}
	for i := range out {
		_ = out[i].Decode()
	}
	return out, nil
}

func (s *Service) FlowVersions(ctx context.Context, key string) ([]engine.Definition, error) {
	out := []engine.Definition{}
	if err := s.conn.QueryRowsPartialCtx(ctx, &out, "SELECT * FROM wf_definition WHERE flow_key=? ORDER BY version DESC", key); err != nil {
		return nil, err
	}
	for i := range out {
		_ = out[i].Decode()
	}
	return out, nil
}

// ---------- lifecycle: draft → (shadow) → enabled ⇄ disabled ----------

type StatusReq struct {
	Action   string `json:"action"`   // start_shadow | enable | disable
	Start    string `json:"start"`    // first enable: from_now | duration
	Duration string `json:"duration"` // rescan window for start=duration
}

func (s *Service) ChangeStatus(ctx context.Context, actor, key string, r StatusReq) (*TaskType, error) {
	tt, err := s.Type(ctx, s.conn, key)
	if err != nil {
		return nil, err
	}
	if (r.Action == "start_shadow" || r.Action == "enable") && (tt.ActiveConfigVersion == 0 || tt.ActiveFlowVersion == 0) {
		return nil, BadRequest("请先发布采集配置和处理流程")
	}
	var shadowTasks []Task
	err = s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
		now := s.nowMs()
		switch r.Action {
		case "start_shadow":
			if tt.Status != TypeDraft && tt.Status != TypeDisabled {
				return BadRequest("只有草稿或停用状态可以进入影子运行")
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_type SET status=?, shadow_started_at=?, updated_at=? WHERE type_key=?", TypeShadow, now, now, key); err != nil {
				return err
			}
			if err := s.startSchedule(ctx, q, key, r, r.Start != ""); err != nil {
				return err
			}
		case "enable":
			if tt.Status == TypeEnabled {
				return BadRequest("已经是启用状态")
			}
			first := tt.EnabledAt == 0
			if first && r.Start == "" {
				return BadRequest("首次启用必须选择起点：只处理启用之后的对象，或回扫指定时长")
			}
			if tt.Status == TypeShadow {
				if err := q.QueryRowsCtx(ctx, &shadowTasks, "SELECT * FROM bw_task WHERE type_key=? AND is_shadow=1 AND status<>'CLOSED'", key); err != nil {
					return err
				}
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_type SET status=?, enabled_at=CASE WHEN enabled_at=0 THEN ? ELSE enabled_at END, updated_at=? WHERE type_key=?", TypeEnabled, now, now, key); err != nil {
				return err
			}
			if err := s.startSchedule(ctx, q, key, r, first || r.Start != ""); err != nil {
				return err
			}
		case "disable":
			if tt.Status != TypeEnabled && tt.Status != TypeShadow {
				return BadRequest("当前状态不能停用")
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_task_type SET status=?, updated_at=? WHERE type_key=?", TypeDisabled, now, key); err != nil {
				return err
			}
			if _, err := q.ExecCtx(ctx, "UPDATE bw_schedule SET status='paused', pause_reason='任务类型已停用', updated_at=? WHERE type_key=?", now, key); err != nil {
				return err
			}
		case "resume_schedule":
			if _, err := q.ExecCtx(ctx, "UPDATE bw_schedule SET status='active', pause_reason='', next_fire_at=?, updated_at=? WHERE type_key=? AND status IN ('paused','tripped')", now, now, key); err != nil {
				return err
			}
		case "pause_schedule":
			if _, err := q.ExecCtx(ctx, "UPDATE bw_schedule SET status='paused', pause_reason='手动暂停', updated_at=? WHERE type_key=?", now, key); err != nil {
				return err
			}
		default:
			return BadRequest("未知操作 %s", r.Action)
		}
		s.Audit(ctx, q, actor, "type_"+r.Action, key, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// enabling ends the shadow run: shadow tasks close without pushing
	for i := range shadowTasks {
		_ = s.terminate(ctx, &shadowTasks[i], CloseShadowEnd, actor, "影子运行结束")
	}
	return s.Type(ctx, s.conn, key)
}

func (s *Service) startSchedule(ctx context.Context, q db.Querier, key string, r StatusReq, setStart bool) error {
	tt, err := s.Type(ctx, q, key)
	if err != nil {
		return err
	}
	cfg, err := s.Config(ctx, q, key, tt.ActiveConfigVersion)
	if err != nil {
		return err
	}
	status := "active"
	if !cfg.Spec.Trigger.Has("sql") || !cfg.Spec.Schedule.Enabled {
		status = "paused"
	}
	now := s.nowMs()
	if setStart {
		watermark := now // only objects entering after enable
		if r.Start == "duration" {
			d, err := expr.ParseDuration(r.Duration)
			if err != nil {
				return BadRequest("回扫时长: %v", err)
			}
			watermark = now - d.Milliseconds()
		}
		if _, err := q.ExecCtx(ctx, "UPDATE bw_schedule SET watermark=? WHERE type_key=?", watermark, key); err != nil {
			return err
		}
	}
	_, err = q.ExecCtx(ctx, "UPDATE bw_schedule SET status=?, pause_reason='', config_version=?, cron=?, timezone=?, next_fire_at=?, updated_at=? WHERE type_key=?",
		status, cfg.Version, cfg.Spec.Schedule.Cron, cfg.Spec.Schedule.Timezone, now, now, key)
	return err
}
