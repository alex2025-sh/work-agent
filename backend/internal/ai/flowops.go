package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"bywork/internal/engine"
	"bywork/internal/task"
)

// FlowOp is one edit to a workflow draft. Proposing ops instead of a whole
// graph keeps diffs small and reviewable, and the model cannot silently drop
// nodes it did not mean to touch.
type FlowOp struct {
	Op       string          `json:"op"` // add_node | update_node | remove_node | connect | disconnect | set_card | remove_card
	Key      string          `json:"key,omitempty"`
	Type     string          `json:"type,omitempty"`
	Name     string          `json:"name,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	After    string          `json:"after,omitempty"` // add_node: place below this node
	From     string          `json:"from,omitempty"`
	Exit     string          `json:"exit,omitempty"`
	To       string          `json:"to,omitempty"`
	Template json.RawMessage `json:"template,omitempty"` // set_card
}

// FlowDoc is the editable part of a workflow version.
type FlowDoc struct {
	Graph  engine.Graph               `json:"graph"`
	Layout map[string]json.RawMessage `json:"layout"`
	Cards  map[string]json.RawMessage `json:"cards"`
}

type pos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

func docFromDefinition(d *engine.Definition) (*FlowDoc, error) {
	doc := &FlowDoc{Graph: d.Graph, Layout: map[string]json.RawMessage{}, Cards: map[string]json.RawMessage{}}
	if len(d.Layout) > 0 {
		if err := json.Unmarshal(d.Layout, &doc.Layout); err != nil {
			return nil, err
		}
	}
	if len(d.Cards) > 0 {
		if err := json.Unmarshal(d.Cards, &doc.Cards); err != nil {
			return nil, err
		}
	}
	// deep copy slices so ops never touch the cached definition
	doc.Graph.Nodes = append([]engine.Node(nil), d.Graph.Nodes...)
	doc.Graph.Edges = append([]engine.Edge(nil), d.Graph.Edges...)
	return doc, nil
}

func (d *FlowDoc) pos(key string) (pos, bool) {
	raw, ok := d.Layout[key]
	if !ok {
		return pos{}, false
	}
	var p pos
	return p, json.Unmarshal(raw, &p) == nil
}

func (d *FlowDoc) place(key, after string) {
	p := pos{X: 0, Y: 0}
	if a, ok := d.pos(after); ok {
		p = pos{X: a.X, Y: a.Y + 140}
	} else {
		for k := range d.Layout {
			if q, ok := d.pos(k); ok && k != "_notes" && q.Y+140 > p.Y {
				p.Y = q.Y + 140
			}
		}
	}
	// nudge right until no node sits at the same spot
	for {
		clash := false
		for k := range d.Layout {
			if q, ok := d.pos(k); ok && k != key && q.X == p.X && q.Y == p.Y {
				clash = true
			}
		}
		if !clash {
			break
		}
		p.X += 280
	}
	b, _ := json.Marshal(p)
	d.Layout[key] = b
}

// Apply applies ops in order; the first invalid op stops with an error that
// names it, so the model can fix that op specifically.
func (d *FlowDoc) Apply(ops []FlowOp, nodeTypes map[string]bool) error {
	for i, op := range ops {
		if err := d.apply(op, nodeTypes); err != nil {
			return fmt.Errorf("ops[%d] (%s): %w", i, op.Op, err)
		}
	}
	return nil
}

func (d *FlowDoc) apply(op FlowOp, nodeTypes map[string]bool) error {
	switch op.Op {
	case "add_node":
		if !keyRe.MatchString(op.Key) {
			return fmt.Errorf("节点 key %q 需为小写字母开头的 [a-z0-9_]", op.Key)
		}
		if d.Graph.Node(op.Key) != nil {
			return fmt.Errorf("节点 %s 已存在", op.Key)
		}
		if !nodeTypes[op.Type] {
			return fmt.Errorf("未知节点类型 %q", op.Type)
		}
		if op.Type == "start" {
			return fmt.Errorf("流程只能有一个开始节点")
		}
		d.Graph.Nodes = append(d.Graph.Nodes, engine.Node{Key: op.Key, Type: op.Type, Name: op.Name, Config: op.Config})
		d.place(op.Key, op.After)
	case "update_node":
		n := d.Graph.Node(op.Key)
		if n == nil {
			return fmt.Errorf("节点 %s 不存在", op.Key)
		}
		if op.Name != "" {
			n.Name = op.Name
		}
		if len(op.Config) > 0 {
			n.Config = op.Config
		}
	case "remove_node":
		n := d.Graph.Node(op.Key)
		if n == nil {
			return fmt.Errorf("节点 %s 不存在", op.Key)
		}
		if n.Type == "start" {
			return fmt.Errorf("不能删除开始节点")
		}
		nodes := d.Graph.Nodes[:0]
		for _, x := range d.Graph.Nodes {
			if x.Key != op.Key {
				nodes = append(nodes, x)
			}
		}
		d.Graph.Nodes = nodes
		edges := d.Graph.Edges[:0]
		for _, e := range d.Graph.Edges {
			if e.From != op.Key && e.To != op.Key {
				edges = append(edges, e)
			}
		}
		d.Graph.Edges = edges
		delete(d.Layout, op.Key)
	case "connect":
		if d.Graph.Node(op.From) == nil || d.Graph.Node(op.To) == nil {
			return fmt.Errorf("连线 %s → %s 引用了不存在的节点", op.From, op.To)
		}
		if op.Exit == "" {
			return fmt.Errorf("需要出口名 exit")
		}
		d.disconnect(op.From, op.Exit)
		d.Graph.Edges = append(d.Graph.Edges, engine.Edge{From: op.From, Exit: op.Exit, To: op.To})
	case "disconnect":
		if !d.disconnect(op.From, op.Exit) {
			return fmt.Errorf("没有 %s 的出口 %s 的连线", op.From, op.Exit)
		}
	case "set_card":
		if !keyRe.MatchString(op.Key) {
			return fmt.Errorf("卡片 key %q 需为小写字母开头的 [a-z0-9_]", op.Key)
		}
		var tpl task.CardTemplate
		if err := json.Unmarshal(op.Template, &tpl); err != nil {
			return fmt.Errorf("卡片模板格式错误: %v", err)
		}
		d.Cards[op.Key] = op.Template
	case "remove_card":
		if _, ok := d.Cards[op.Key]; !ok {
			return fmt.Errorf("卡片 %s 不存在", op.Key)
		}
		delete(d.Cards, op.Key)
	default:
		return fmt.Errorf("未知操作 %q", op.Op)
	}
	return nil
}

func (d *FlowDoc) disconnect(from, exit string) bool {
	found := false
	edges := d.Graph.Edges[:0]
	for _, e := range d.Graph.Edges {
		if e.From == from && e.Exit == exit {
			found = true
			continue
		}
		edges = append(edges, e)
	}
	d.Graph.Edges = edges
	return found
}

// Definition renders the doc back into a definition for validation.
func (d *FlowDoc) Definition() *engine.Definition {
	layout, _ := json.Marshal(d.Layout)
	cards, _ := json.Marshal(d.Cards)
	return &engine.Definition{Graph: d.Graph, Layout: layout, Cards: cards}
}

// FlowChange summarises what a proposal changes, for the reviewer.
type FlowChange struct {
	AddedNodes   []string `json:"added_nodes"`
	ChangedNodes []string `json:"changed_nodes"`
	RemovedNodes []string `json:"removed_nodes"`
	AddedEdges   []string `json:"added_edges"`
	RemovedEdges []string `json:"removed_edges"`
	ChangedCards []string `json:"changed_cards"`
}

func diffFlow(before, after *FlowDoc) FlowChange {
	c := FlowChange{AddedNodes: []string{}, ChangedNodes: []string{}, RemovedNodes: []string{}, AddedEdges: []string{}, RemovedEdges: []string{}, ChangedCards: []string{}}
	bn := map[string]engine.Node{}
	for _, n := range before.Graph.Nodes {
		bn[n.Key] = n
	}
	an := map[string]engine.Node{}
	for _, n := range after.Graph.Nodes {
		an[n.Key] = n
		old, ok := bn[n.Key]
		switch {
		case !ok:
			c.AddedNodes = append(c.AddedNodes, n.Key)
		case old.Name != n.Name || old.Type != n.Type || !jsonEqual(old.Config, n.Config):
			c.ChangedNodes = append(c.ChangedNodes, n.Key)
		}
	}
	for k := range bn {
		if _, ok := an[k]; !ok {
			c.RemovedNodes = append(c.RemovedNodes, k)
		}
	}
	edgeKey := func(e engine.Edge) string { return e.From + ":" + e.Exit + "→" + e.To }
	be := map[string]bool{}
	for _, e := range before.Graph.Edges {
		be[edgeKey(e)] = true
	}
	ae := map[string]bool{}
	for _, e := range after.Graph.Edges {
		ae[edgeKey(e)] = true
		if !be[edgeKey(e)] {
			c.AddedEdges = append(c.AddedEdges, edgeKey(e))
		}
	}
	for k := range be {
		if !ae[k] {
			c.RemovedEdges = append(c.RemovedEdges, k)
		}
	}
	for k, v := range after.Cards {
		if old, ok := before.Cards[k]; !ok || !jsonEqual(old, v) {
			c.ChangedCards = append(c.ChangedCards, k)
		}
	}
	for k := range before.Cards {
		if _, ok := after.Cards[k]; !ok {
			c.ChangedCards = append(c.ChangedCards, k)
		}
	}
	for _, s := range [][]string{c.AddedNodes, c.ChangedNodes, c.RemovedNodes, c.AddedEdges, c.RemovedEdges, c.ChangedCards} {
		sort.Strings(s)
	}
	return c
}

func (c FlowChange) Empty() bool {
	return len(c.AddedNodes)+len(c.ChangedNodes)+len(c.RemovedNodes)+len(c.AddedEdges)+len(c.RemovedEdges)+len(c.ChangedCards) == 0
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if len(a) == 0 {
		a = json.RawMessage("null")
	}
	if len(b) == 0 {
		b = json.RawMessage("null")
	}
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}
