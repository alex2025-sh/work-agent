package engine

import (
	"fmt"
	"strings"

	"bywork/internal/expr"
)

// systemPrefixes are runtime-provided, never part of the input contract.
var systemPrefixes = []string{"now", "node.", "task.", "run.", "action.", "next.", "actor", "time"}

func isSystem(f string) bool {
	for _, p := range systemPrefixes {
		if f == p || strings.HasPrefix(f, p) {
			return true
		}
	}
	return false
}

type Validation struct {
	Issues []FieldError `json:"issues"`
	// Inputs is the flow's input contract: snapshot fields it needs.
	Inputs []string `json:"inputs"`
}

func (v *Validation) OK() bool {
	for _, i := range v.Issues {
		if i.Level == "error" {
			return false
		}
	}
	return true
}

func (v *Validation) add(level, node, msg string, args ...any) {
	v.Issues = append(v.Issues, FieldError{Node: node, Level: level, Message: fmt.Sprintf(msg, args...)})
}

// Validate checks structure, node configs and computes the input contract.
func (e *Engine) Validate(g *Graph, vc *ValidateCtx) *Validation {
	v := &Validation{}
	keys := map[string]bool{}
	starts := 0
	for _, n := range g.Nodes {
		if n.Key == "" {
			v.add("error", "", "存在没有 key 的节点")
			continue
		}
		if keys[n.Key] {
			v.add("error", n.Key, "节点 key 重复")
		}
		keys[n.Key] = true
		if n.Type == "start" {
			starts++
		}
		if _, ok := e.Executor(n.Type); !ok {
			v.add("error", n.Key, "未知节点类型 %s", n.Type)
		}
	}
	if starts != 1 {
		v.add("error", "", "流程必须有且只有一个开始节点（当前 %d 个）", starts)
	}
	for _, ed := range g.Edges {
		if !keys[ed.From] || !keys[ed.To] {
			v.add("error", ed.From, "连线指向不存在的节点 %s → %s", ed.From, ed.To)
		}
	}
	// exits
	for i := range g.Nodes {
		n := &g.Nodes[i]
		x, ok := e.Executor(n.Type)
		if !ok {
			continue
		}
		for _, fe := range x.ValidateConfig(n, vc) {
			if fe.Node == "" {
				fe.Node = n.Key
			}
			if fe.Level == "" {
				fe.Level = "error"
			}
			v.Issues = append(v.Issues, fe)
		}
		exits := x.Exits(n)
		declared := map[string]bool{}
		for _, ex := range exits {
			declared[ex] = true
			if _, ok := g.Next(n.Key, ex); !ok {
				v.add("error", n.Key, "「%s」的出口 %q 没有去向", label(n), ex)
			}
		}
		seen := map[string]bool{}
		for _, ed := range g.Edges {
			if ed.From != n.Key {
				continue
			}
			if !declared[ed.Exit] {
				v.add("error", n.Key, "「%s」没有名为 %q 的出口", label(n), ed.Exit)
			}
			if seen[ed.Exit] {
				v.add("error", n.Key, "「%s」的出口 %q 连了多条线", label(n), ed.Exit)
			}
			seen[ed.Exit] = true
		}
	}
	// reachability
	if st := g.Start(); st != nil {
		reach := map[string]bool{st.Key: true}
		queue := []string{st.Key}
		for len(queue) > 0 {
			k := queue[0]
			queue = queue[1:]
			for _, ed := range g.Edges {
				if ed.From == k && !reach[ed.To] {
					reach[ed.To] = true
					queue = append(queue, ed.To)
				}
			}
		}
		for _, n := range g.Nodes {
			if !reach[n.Key] {
				v.add("error", n.Key, "「%s」从开始节点不可达", label(&n))
			}
		}
	}

	// input contract: reads not written by any ancestor data node
	var inputs []string
	for i := range g.Nodes {
		n := &g.Nodes[i]
		x, ok := e.Executor(n.Type)
		if !ok {
			continue
		}
		reads, _ := x.Fields(n)
		anc := g.ancestors(n.Key)
		for _, f := range reads {
			if isSystem(f) {
				continue
			}
			if !e.writtenBy(g, anc, f) {
				inputs = append(inputs, f)
			}
		}
	}
	v.Inputs = expr.Uniq(inputs)

	// hard rule: decisions after a wait must use live reads
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Type != "condition" {
			continue
		}
		x, _ := e.Executor(n.Type)
		reads, _ := x.Fields(n)
		for _, f := range reads {
			if isSystem(f) {
				continue
			}
			if e.snapshotAfterWait(g, n.Key, f) {
				v.add("warning", n.Key, "「%s」在等待之后用快照字段 %s 做判断，应先用取数节点实时读取", label(n), f)
			}
		}
	}
	if v.OK() {
		v.add("info", "", "通过 · 所有节点可达，每个出口都有去向")
	}
	return v
}

func label(n *Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.Key
}

func (g *Graph) ancestors(key string) map[string]bool {
	out := map[string]bool{}
	queue := []string{key}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		for _, p := range g.preds(k) {
			if !out[p] {
				out[p] = true
				queue = append(queue, p)
			}
		}
	}
	return out
}

func (e *Engine) writtenBy(g *Graph, nodes map[string]bool, field string) bool {
	for k := range nodes {
		n := g.Node(k)
		if n == nil {
			continue
		}
		x, ok := e.Executor(n.Type)
		if !ok {
			continue
		}
		_, writes := x.Fields(n)
		for _, w := range writes {
			if w == field {
				return true
			}
		}
	}
	return false
}

// snapshotAfterWait walks backwards from a node; it reports true if some path
// reaches a suspending node (wait / human) before a node that refreshes field.
func (e *Engine) snapshotAfterWait(g *Graph, from, field string) bool {
	seen := map[string]bool{}
	var walk func(k string) bool
	walk = func(k string) bool {
		for _, p := range g.preds(k) {
			if seen[p] {
				continue
			}
			seen[p] = true
			n := g.Node(p)
			if n == nil {
				continue
			}
			if x, ok := e.Executor(n.Type); ok {
				_, writes := x.Fields(n)
				refreshed := false
				for _, w := range writes {
					if w == field {
						refreshed = true
					}
				}
				if refreshed {
					continue
				}
			}
			if n.Type == "wait" || n.Type == "human" {
				return true
			}
			if walk(p) {
				return true
			}
		}
		return false
	}
	return walk(from)
}
