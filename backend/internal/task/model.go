package task

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"bywork/internal/expr"
)

// Task status (design §对象模型).
const (
	StatusOpen       = "OPEN"       // no active human work item
	StatusProcessing = "PROCESSING" // an active work item was delivered
	StatusClosed     = "CLOSED"
)

// Close reasons.
const (
	CloseCompleted   = "completed"        // 处理完成
	CloseInvalidated = "invalidated"      // Case 已失效
	CloseAdmin       = "admin_terminated" // 管理员终止
	CloseFlowFailed  = "flow_failed"      // 流程执行失败
	CloseShadowEnd   = "shadow_ended"     // 影子运行结束
)

var CloseReasonText = map[string]string{
	CloseCompleted: "处理完成", CloseInvalidated: "Case 已失效", CloseAdmin: "管理员终止",
	CloseFlowFailed: "流程执行失败", CloseShadowEnd: "影子运行结束",
}

// Attention flags.
const (
	AttnRMMissing      = "rm_missing"       // RM 映射缺失
	AttnDeliveryFailed = "delivery_failed"  // 投递失败
	AttnReadFailed     = "data_read_failed" // 数据读取失败
	AttnFlowError      = "flow_error"
	AttnAssigneeLeft   = "assignee_left" // 处理人离职 / 移出团队
)

var AttentionText = map[string]string{
	AttnRMMissing: "RM 映射缺失", AttnDeliveryFailed: "投递失败", AttnReadFailed: "数据读取失败",
	AttnFlowError: "流程执行异常", AttnAssigneeLeft: "处理人已离开团队",
}

// TaskType status.
const (
	TypeDraft    = "draft"
	TypeShadow   = "shadow"
	TypeEnabled  = "enabled"
	TypeDisabled = "disabled"
)

type TaskType struct {
	ID                  int64  `db:"id" json:"id"`
	TypeKey             string `db:"type_key" json:"type_key"`
	Name                string `db:"name" json:"name"`
	Description         string `db:"description" json:"description"`
	Team                string `db:"team" json:"team"`
	Visibility          string `db:"visibility" json:"visibility"`
	ObjectType          string `db:"object_type" json:"object_type"`
	DedupeFieldsJSON    string `db:"dedupe_fields" json:"-"`
	RoundField          string `db:"round_field" json:"round_field"`
	Status              string `db:"status" json:"status"`
	Creator             string `db:"creator" json:"creator"`
	ActiveConfigVersion int64  `db:"active_config_version" json:"active_config_version"`
	ActiveFlowVersion   int64  `db:"active_flow_version" json:"active_flow_version"`
	ShadowStartedAt     int64  `db:"shadow_started_at" json:"shadow_started_at"`
	EnabledAt           int64  `db:"enabled_at" json:"enabled_at"`
	CreatedAt           int64  `db:"created_at" json:"created_at"`
	UpdatedAt           int64  `db:"updated_at" json:"updated_at"`
}

func (t *TaskType) DedupeFields() []string {
	out := []string{}
	_ = json.Unmarshal([]byte(t.DedupeFieldsJSON), &out)
	return out
}

// FlowKey: phase 1 flows are not shared across task types.
func (t *TaskType) FlowKey() string { return t.TypeKey }

// DedupeKey builds the task identity from snapshot values.
func (t *TaskType) DedupeKey(v expr.Vars, shadow bool) (string, error) {
	var parts []string
	for _, f := range append(t.DedupeFields(), nonEmpty(t.RoundField)...) {
		s := v.String(f)
		if s == "" {
			return "", fmt.Errorf("去重字段 %s 为空", f)
		}
		parts = append(parts, s)
	}
	k := strings.Join(parts, "|")
	if shadow {
		k = "shadow:" + k // separate namespace so enabling later still creates real tasks
	}
	return k, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

type ConfigRow struct {
	ID          int64  `db:"id" json:"id"`
	TypeKey     string `db:"type_key" json:"type_key"`
	Version     int64  `db:"version" json:"version"`
	Status      string `db:"status" json:"status"`
	BaseVersion int64  `db:"base_version" json:"base_version"`
	Revision    int64  `db:"revision" json:"revision"`
	SpecJSON    string `db:"spec" json:"-"`
	Note        string `db:"note" json:"note"`
	Author      string `db:"author" json:"author"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
	PublishedAt int64  `db:"published_at" json:"published_at"`

	Spec *ConfigSpec `db:"-" json:"spec"`
}

func (c *ConfigRow) Decode() error {
	c.Spec = &ConfigSpec{}
	return json.Unmarshal([]byte(c.SpecJSON), c.Spec)
}

// ConfigSpec is TaskConfig vN: when a task is produced.
type ConfigSpec struct {
	Trigger      Trigger      `json:"trigger"`
	Fields       []FieldMap   `json:"fields"`
	Eligibility  expr.Group   `json:"eligibility"`
	LiveRefresh  []Refresh    `json:"live_refresh"`
	Invalidation Invalidation `json:"invalidation"`
	Schedule     Schedule     `json:"schedule"`
	MaxPerRun    int          `json:"max_per_run"`
}

type Trigger struct {
	Sources    []string `json:"sources"` // sql | push | mcp | flow
	Datasource string   `json:"datasource,omitempty"`
	Query      string   `json:"query,omitempty"`
	PushEvent  string   `json:"push_event,omitempty"`
	FlowKey    string   `json:"flow_key,omitempty"`
	MCPTool    string   `json:"mcp_tool,omitempty"`
}

func (t Trigger) Has(s string) bool {
	for _, x := range t.Sources {
		if x == s {
			return true
		}
	}
	return false
}

type FieldMap struct {
	Column      string `json:"column"`
	Field       string `json:"field"`
	Type        string `json:"type"` // string | number | bool | datetime
	Required    bool   `json:"required"`
	Sensitivity string `json:"sensitivity"` // normal | sensitive
	Label       string `json:"label,omitempty"`
}

type Refresh struct {
	Capability string            `json:"capability"`
	Args       map[string]string `json:"args"`
}

type Invalidation struct {
	Events       []string `json:"events"`        // e.g. case.closed
	RecheckEvery string   `json:"recheck_every"` // e.g. 6h; empty = only on events / reminders
}

type Schedule struct {
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
}

// Produced is the set of snapshot fields this config version writes.
func (s *ConfigSpec) Produced() []string {
	out := []string{}
	for _, f := range s.Fields {
		out = append(out, f.Field)
	}
	return expr.Uniq(out)
}

func (s *ConfigSpec) Sensitive() map[string]bool {
	m := map[string]bool{}
	for _, f := range s.Fields {
		if f.Sensitivity == "sensitive" {
			m[f.Field] = true
		}
	}
	return m
}

type Task struct {
	ID             int64  `db:"id" json:"id"`
	TypeKey        string `db:"type_key" json:"type_key"`
	DedupeKey      string `db:"dedupe_key" json:"dedupe_key"`
	ObjectKey      string `db:"object_key" json:"object_key"`
	IsShadow       int64  `db:"is_shadow" json:"is_shadow"`
	ConfigVersion  int64  `db:"config_version" json:"config_version"`
	FlowVersion    int64  `db:"flow_version" json:"flow_version"`
	RunID          int64  `db:"run_id" json:"run_id"`
	Title          string `db:"title" json:"title"`
	Team           string `db:"team" json:"team"`
	SnapshotJSON   string `db:"snapshot" json:"-"`
	OriginJSON     string `db:"origin" json:"-"`
	Status         string `db:"status" json:"status"`
	CloseReason    string `db:"close_reason" json:"close_reason"`
	Attention      string `db:"attention" json:"attention"`
	Assignee       string `db:"assignee" json:"assignee"`
	NextActionAt   int64  `db:"next_action_at" json:"next_action_at"`
	NextActionText string `db:"next_action_text" json:"next_action_text"`
	LarkMessageID  string `db:"lark_message_id" json:"-"`
	CardRev        int64  `db:"card_rev" json:"card_rev"`
	DelayCount     int64  `db:"delay_count" json:"delay_count"`
	LastCheckedAt  int64  `db:"last_checked_at" json:"last_checked_at"`
	CreatedAt      int64  `db:"created_at" json:"created_at"`
	UpdatedAt      int64  `db:"updated_at" json:"updated_at"`
	ClosedAt       int64  `db:"closed_at" json:"closed_at"`
}

func (t *Task) Snapshot() expr.Vars {
	v := expr.Vars{}
	_ = json.Unmarshal([]byte(t.SnapshotJSON), &v)
	return v
}

// Origin explains "为什么有这个任务".
type Origin struct {
	Source        string   `json:"source"` // scan | push | manual | backfill
	At            int64    `json:"at"`
	EventID       string   `json:"event_id,omitempty"`
	ConfigVersion int64    `json:"config_version"`
	Matched       []string `json:"matched"`
	Actor         string   `json:"actor,omitempty"`
}

func (t *Task) Origin() Origin {
	var o Origin
	_ = json.Unmarshal([]byte(t.OriginJSON), &o)
	return o
}

func (t *Task) AttentionFlags() []string { return splitFlags(t.Attention) }

func splitFlags(s string) []string {
	out := []string{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func withFlag(s, f string, on bool) string {
	set := map[string]bool{}
	for _, x := range splitFlags(s) {
		set[x] = true
	}
	if on {
		set[f] = true
	} else {
		delete(set, f)
	}
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

type WorkItem struct {
	ID               int64  `db:"id" json:"id"`
	TaskID           int64  `db:"task_id" json:"task_id"`
	RunID            int64  `db:"run_id" json:"run_id"`
	NodeKey          string `db:"node_key" json:"node_key"`
	Round            int64  `db:"round" json:"round"`
	Assignee         string `db:"assignee" json:"assignee"`
	AssignReasonJSON string `db:"assign_reason" json:"-"`
	Status           string `db:"status" json:"status"` // active | done | delayed | voided
	CardJSON         string `db:"card" json:"-"`
	CardRev          int64  `db:"card_rev" json:"card_rev"`
	ButtonsJSON      string `db:"buttons" json:"-"`
	Action           string `db:"action" json:"action"`
	ActionBy         string `db:"action_by" json:"action_by"`
	ActionNote       string `db:"action_note" json:"action_note"`
	CreatedAt        int64  `db:"created_at" json:"created_at"`
	DeliveredAt      int64  `db:"delivered_at" json:"delivered_at"`
	DoneAt           int64  `db:"done_at" json:"done_at"`
}

// AssignReason explains "为什么是我".
type AssignReason struct {
	Rule       string `json:"rule"`
	Team       string `json:"team"`
	Via        string `json:"via"` // mapping | override | fixed | field | leader | missing
	OverrideID int64  `json:"override_id,omitempty"`
	Approval   string `json:"approval,omitempty"`
	By         string `json:"by,omitempty"` // leader who reassigned
	Note       string `json:"note,omitempty"`
}

func (w *WorkItem) Reason() AssignReason {
	var r AssignReason
	_ = json.Unmarshal([]byte(w.AssignReasonJSON), &r)
	return r
}

type Member struct {
	ID         int64  `db:"id" json:"id"`
	UserID     string `db:"user_id" json:"user_id"`
	Name       string `db:"name" json:"name"`
	Team       string `db:"team" json:"team"`
	Role       string `db:"role" json:"role"` // rm | leader | config | admin
	LarkOpenID string `db:"lark_open_id" json:"lark_open_id"`
	Active     int64  `db:"active" json:"active"`
}

type Override struct {
	ID         int64  `db:"id" json:"id"`
	UID        string `db:"uid" json:"uid"`
	RMUser     string `db:"rm_user" json:"rm_user"`
	ApprovalNo string `db:"approval_no" json:"approval_no"`
	ValidUntil int64  `db:"valid_until" json:"valid_until"`
	Revoked    int64  `db:"revoked" json:"revoked"`
	CreatedBy  string `db:"created_by" json:"created_by"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
}

type LogEntry struct {
	ID     int64  `db:"id" json:"id"`
	TaskID int64  `db:"task_id" json:"task_id"`
	At     int64  `db:"at" json:"at"`
	Kind   string `db:"kind" json:"kind"`
	Actor  string `db:"actor" json:"actor"`
	Text   string `db:"text" json:"text"`
}

type ScheduleRow struct {
	ID                  int64  `db:"id" json:"id"`
	TypeKey             string `db:"type_key" json:"type_key"`
	ConfigVersion       int64  `db:"config_version" json:"config_version"`
	Cron                string `db:"cron" json:"cron"`
	Timezone            string `db:"timezone" json:"timezone"`
	Status              string `db:"status" json:"status"` // active | paused | tripped
	PauseReason         string `db:"pause_reason" json:"pause_reason"`
	NextFireAt          int64  `db:"next_fire_at" json:"next_fire_at"`
	Watermark           int64  `db:"watermark" json:"watermark"`
	Checkpoint          string `db:"checkpoint" json:"checkpoint"`
	LeaseOwner          string `db:"lease_owner" json:"-"`
	LeaseUntil          int64  `db:"lease_until" json:"-"`
	LastRunAt           int64  `db:"last_run_at" json:"last_run_at"`
	LastSuccessAt       int64  `db:"last_success_at" json:"last_success_at"`
	ConsecutiveFailures int64  `db:"consecutive_failures" json:"consecutive_failures"`
	UpdatedAt           int64  `db:"updated_at" json:"updated_at"`
}

type ScheduleRun struct {
	ID            int64  `db:"id" json:"id"`
	TypeKey       string `db:"type_key" json:"type_key"`
	ConfigVersion int64  `db:"config_version" json:"config_version"`
	Source        string `db:"source" json:"source"`
	FiredAt       int64  `db:"fired_at" json:"fired_at"`
	DurationMs    int64  `db:"duration_ms" json:"duration_ms"`
	RowsScanned   int64  `db:"rows_scanned" json:"rows_scanned"`
	TasksCreated  int64  `db:"tasks_created" json:"tasks_created"`
	Result        string `db:"result" json:"result"`
	Message       string `db:"message" json:"message"`
}
