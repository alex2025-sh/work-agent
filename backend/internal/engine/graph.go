package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"bywork/internal/db"
	"bywork/internal/expr"
)

// Graph holds nodes, config and edges only — never coordinates. Validation,
// version diffs and AI generation look at Graph; Layout is stored separately.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type Node struct {
	Key    string          `json:"key"`
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config,omitempty"`
}

// Edge connects a named exit of From to To.
type Edge struct {
	From string `json:"from"`
	Exit string `json:"exit"`
	To   string `json:"to"`
}

func (g *Graph) Node(key string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].Key == key {
			return &g.Nodes[i]
		}
	}
	return nil
}

func (g *Graph) Next(from, exit string) (string, bool) {
	for _, e := range g.Edges {
		if e.From == from && e.Exit == exit {
			return e.To, true
		}
	}
	return "", false
}

func (g *Graph) Start() *Node {
	for i := range g.Nodes {
		if g.Nodes[i].Type == "start" {
			return &g.Nodes[i]
		}
	}
	return nil
}

func (g *Graph) preds(key string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.To == key {
			out = append(out, e.From)
		}
	}
	return out
}

// Definition is one version of a workflow (wf_definition row).
type Definition struct {
	ID          int64  `db:"id" json:"id"`
	FlowKey     string `db:"flow_key" json:"flow_key"`
	Version     int64  `db:"version" json:"version"`
	Status      string `db:"status" json:"status"`
	BaseVersion int64  `db:"base_version" json:"base_version"`
	Revision    int64  `db:"revision" json:"revision"`
	GraphJSON   string `db:"graph" json:"-"`
	LayoutJSON  string `db:"layout" json:"-"`
	CardsJSON   string `db:"cards" json:"-"`
	Note        string `db:"note" json:"note"`
	Author      string `db:"author" json:"author"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
	PublishedAt int64  `db:"published_at" json:"published_at"`

	Graph  Graph           `db:"-" json:"graph"`
	Layout json.RawMessage `db:"-" json:"layout"`
	Cards  json.RawMessage `db:"-" json:"cards"`
}

func (d *Definition) Decode() error {
	if err := json.Unmarshal([]byte(d.GraphJSON), &d.Graph); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	d.Layout = json.RawMessage(orEmpty(d.LayoutJSON))
	d.Cards = json.RawMessage(orEmpty(d.CardsJSON))
	return nil
}

func orEmpty(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

func LoadDefinition(ctx context.Context, q db.Querier, flowKey string, version int64) (*Definition, error) {
	var d Definition
	if err := q.QueryRowPartialCtx(ctx, &d, "SELECT * FROM wf_definition WHERE flow_key = ? AND version = ?", flowKey, version); err != nil {
		return nil, err
	}
	return &d, d.Decode()
}

// ---------- runtime records ----------

type Run struct {
	ID          int64  `db:"id" json:"id"`
	FlowKey     string `db:"flow_key" json:"flow_key"`
	FlowVersion int64  `db:"flow_version" json:"flow_version"`
	TaskID      int64  `db:"task_id" json:"task_id"`
	Status      string `db:"status" json:"status"`
	VarsJSON    string `db:"vars" json:"-"`
	ResultJSON  string `db:"result" json:"-"`
	LeaseOwner  string `db:"lease_owner" json:"-"`
	LeaseUntil  int64  `db:"lease_until" json:"-"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	UpdatedAt   int64  `db:"updated_at" json:"updated_at"`
}

func (r *Run) Vars() expr.Vars {
	v := expr.Vars{}
	_ = json.Unmarshal([]byte(r.VarsJSON), &v)
	return v
}

type Token struct {
	ID         int64  `db:"id" json:"id"`
	RunID      int64  `db:"run_id" json:"run_id"`
	NodeKey    string `db:"node_key" json:"node_key"`
	Status     string `db:"status" json:"status"`
	Seq        int64  `db:"seq" json:"seq"`
	StateJSON  string `db:"state" json:"-"`
	WaitReason string `db:"wait_reason" json:"wait_reason"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
	UpdatedAt  int64  `db:"updated_at" json:"updated_at"`
}

func (t *Token) State() map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(t.StateJSON), &m)
	return m
}

type NodeExec struct {
	ID         int64  `db:"id" json:"id"`
	RunID      int64  `db:"run_id" json:"run_id"`
	TokenID    int64  `db:"token_id" json:"token_id"`
	NodeKey    string `db:"node_key" json:"node_key"`
	Seq        int64  `db:"seq" json:"seq"`
	Status     string `db:"status" json:"status"`
	Input      string `db:"input" json:"input"`
	Output     string `db:"output" json:"output"`
	ExitName   string `db:"exit_name" json:"exit"`
	Error      string `db:"error" json:"error"`
	StartedAt  int64  `db:"started_at" json:"started_at"`
	FinishedAt int64  `db:"finished_at" json:"finished_at"`
	DurationMs int64  `db:"duration_ms" json:"duration_ms"`
}

type Timer struct {
	ID         int64  `db:"id" json:"id"`
	RunID      int64  `db:"run_id" json:"run_id"`
	TokenID    int64  `db:"token_id" json:"token_id"`
	TokenSeq   int64  `db:"token_seq" json:"token_seq"`
	NodeKey    string `db:"node_key" json:"node_key"`
	TimerKey   string `db:"timer_key" json:"timer_key"`
	DueAt      int64  `db:"due_at" json:"due_at"`
	Status     string `db:"status" json:"status"`
	LeaseOwner string `db:"lease_owner" json:"-"`
	LeaseUntil int64  `db:"lease_until" json:"-"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
}

// ---------- executor contract ----------

// NodeInput is what an executor sees. Q is the transaction the engine will
// commit together with the executor's result, so side effects (work items,
// outbox rows, task status) are atomic with the checkpoint.
type NodeInput struct {
	Q      db.Querier
	Run    *Run
	Token  *Token
	Node   *Node
	Def    *Definition
	Vars   expr.Vars
	State  map[string]any
	Now    time.Time
	Event  *Event // set on Resume
	Engine *Engine
}

// NodeResult: exactly one of Exit / Suspend / Terminate should be set.
type NodeResult struct {
	Exit      string
	Output    map[string]any
	State     map[string]any
	Suspend   *Suspend
	Terminate map[string]any // run result, e.g. {"close_reason": "completed"}
}

type Suspend struct {
	Reason       string
	Timers       []TimerSpec
	CancelTimers []string // timer keys (without seq prefix) to cancel
	Ignored      bool     // event was not applicable; nothing changed
}

type TimerSpec struct {
	Key   string
	DueAt time.Time
}

type NodeMeta struct {
	Type     string          `json:"type"`
	Title    string          `json:"title"`
	Category string          `json:"category"` // phase1 | extension
	Desc     string          `json:"desc"`
	Schema   json.RawMessage `json:"schema"`
	Color    string          `json:"color"`
}

type FieldError struct {
	Node    string `json:"node"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Level   string `json:"level"` // error | warning | info
}

type ValidateCtx struct {
	Capabilities map[string][]string // capability -> outputs
	Extra        map[string]any      // host-specific (e.g. card keys)
}

type NodeExecutor interface {
	Meta() NodeMeta
	ValidateConfig(n *Node, vc *ValidateCtx) []FieldError
	Exits(n *Node) []string
	// Fields returns the fields a node reads and the fields it writes (live).
	Fields(n *Node) (reads, writes []string)
	Execute(ctx context.Context, in *NodeInput) (NodeResult, error)
}

// Resumer is implemented by nodes that suspend and wait for events.
type Resumer interface {
	Resume(ctx context.Context, in *NodeInput) (NodeResult, error)
}

// Event wakes a waiting token: human action, timer due, or (reserved) callback.
type Event struct {
	Kind     string         `json:"kind"` // action | timer | callback
	Key      string         `json:"key"`  // idempotency key, e.g. action:<request_key>
	NodeKey  string         `json:"node_key"`
	TokenSeq int64          `json:"token_seq"`
	TimerKey string         `json:"timer_key,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

// Listener lets the task domain react to run lifecycle in the same tx.
type Listener interface {
	OnRunEnd(ctx context.Context, q db.Querier, run *Run, result map[string]any) error
	OnRunError(ctx context.Context, q db.Querier, run *Run, nodeKey string, err error) error
	OnSuspend(ctx context.Context, q db.Querier, run *Run, node *Node, timers []TimerSpec) error
}
