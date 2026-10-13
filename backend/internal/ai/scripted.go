package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"bywork/internal/expr"
	"bywork/internal/task"
)

// Scripted is a deterministic stand-in for a model, for local demos and
// tests. It understands two requests and drives the same tool loop a real
// model does (get_context → propose_* → summary):
//
//	"把 48 小时改成 72 小时"  — changes the elapsed_gte threshold in eligibility
//	"卡片加 case.vip_level"   — adds a field to the first card template
type Scripted struct{}

func (Scripted) Name() string { return "脚本模拟（非 AI）" }

var (
	reDuration = regexp.MustCompile(`(\d+)\s*(小时|h|天|d|分钟|m)\b?.*?(改成|改为|调整为|调到|换成)\s*(\d+)\s*(小时|h|天|d|分钟|m)`)
	reCard     = regexp.MustCompile(`卡片.*?(加|增加|添加|展示)(?:字段|一个字段)?\s*([a-z_][a-z0-9_]*\.[a-z0-9_.]+)(?:[（(]([^）)]+)[）)])?`)
)

func unit(u string) string {
	switch u {
	case "小时", "h":
		return "h"
	case "天", "d":
		return "d"
	}
	return "m"
}

func (s Scripted) Chat(ctx context.Context, req ChatRequest, onDelta func(string)) (*Completion, error) {
	// find the last user message and the tool results after it
	last := -1
	for i, m := range req.Messages {
		if m.Role == "user" {
			last = i
		}
	}
	if last < 0 {
		return s.say(onDelta, "你好，我是配置助手。"), nil
	}
	ask := req.Messages[last].Content
	results := map[string]json.RawMessage{}
	for _, m := range req.Messages[last+1:] {
		if m.Role == "tool" {
			var w struct {
				R json.RawMessage `json:"bywork_tool_result"`
			}
			_ = json.Unmarshal([]byte(m.Content), &w)
			results[m.Name] = w.R
		}
	}
	if r, ok := firstOf(results, "propose_config", "propose_flow"); ok {
		var out struct {
			ProposalID int64       `json:"proposal_id"`
			Validation checkResult `json:"validation"`
			Error      string      `json:"error"`
		}
		_ = json.Unmarshal(r, &out)
		switch {
		case out.Error != "":
			return s.say(onDelta, "提交提议时出错："+out.Error), nil
		case !out.Validation.OK:
			return s.say(onDelta, "提议没有通过校验："+strings.Join(out.Validation.Errors, "；")+"。请调整需求后再试。"), nil
		}
		msg := fmt.Sprintf("已生成提议 #%d，校验通过。", out.ProposalID)
		if len(out.Validation.Warnings) > 0 {
			msg += "提示：" + strings.Join(out.Validation.Warnings, "；") + "。"
		}
		msg += "请在提议卡片里查看 diff，确认后点「接受到草稿」；发布仍需在版本页单独操作。"
		return s.say(onDelta, msg), nil
	}
	ctxRaw, ok := results["get_context"]
	if !ok {
		return s.call("get_context", "{}"), nil
	}
	var c struct {
		Config struct {
			Spec task.ConfigSpec `json:"spec"`
		} `json:"config"`
		Flow struct {
			Cards map[string]task.CardTemplate `json:"cards"`
		} `json:"flow"`
	}
	if err := json.Unmarshal(ctxRaw, &c); err != nil {
		return s.say(onDelta, "读取上下文失败："+err.Error()), nil
	}

	if m := reDuration.FindStringSubmatch(ask); m != nil {
		from, to := m[1]+unit(m[2]), m[4]+unit(m[5])
		spec := c.Config.Spec
		changed := replaceElapsed(spec.Eligibility.All, from, to) + replaceElapsed(spec.Eligibility.Any, from, to)
		if changed == 0 {
			return s.say(onDelta, fmt.Sprintf("资格条件里没有找到「%s」的时长条件，没有生成提议。", from)), nil
		}
		args, _ := json.Marshal(map[string]any{"summary": fmt.Sprintf("资格条件时长 %s → %s", from, to), "spec": spec})
		return s.call("propose_config", string(args)), nil
	}
	if m := reCard.FindStringSubmatch(ask); m != nil {
		field, label := m[2], m[3]
		if label == "" {
			label = field[strings.LastIndex(field, ".")+1:]
		}
		key := ""
		for k := range c.Flow.Cards {
			if key == "" || k < key {
				key = k
			}
		}
		if key == "" {
			return s.say(onDelta, "流程里还没有卡片模板。"), nil
		}
		tpl := c.Flow.Cards[key]
		for _, f := range tpl.Fields {
			if f.Field == field {
				return s.say(onDelta, fmt.Sprintf("卡片「%s」已经有字段 %s。", key, field)), nil
			}
		}
		tpl.Fields = append(tpl.Fields, task.CardField{Field: field, Label: label, Format: "text", Lark: true, List: true, Missing: "—"})
		args, _ := json.Marshal(map[string]any{"summary": fmt.Sprintf("卡片 %s 增加字段 %s", key, field),
			"ops": []map[string]any{{"op": "set_card", "key": key, "template": tpl}}})
		return s.call("propose_flow", string(args)), nil
	}
	return s.say(onDelta, "当前是脚本模拟模式（未接入模型），只支持两类示例：\n· 「把 48 小时改成 72 小时」\n· 「卡片加 case.vip_level（VIP 等级）」\n接入 OpenClaw 后可以用自然语言描述任意修改。"), nil
}

func replaceElapsed(rules []expr.Rule, from, to string) int {
	n := 0
	for i := range rules {
		if (rules[i].Op == "elapsed_gte" || rules[i].Op == "elapsed_lt") && fmt.Sprint(rules[i].Value) == from {
			rules[i].Value = to
			rules[i].Label = strings.ReplaceAll(rules[i].Label, strings.TrimSuffix(from, "h"), strings.TrimSuffix(to, "h"))
			n++
		}
	}
	return n
}

func firstOf(m map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return nil, false
}

func (Scripted) call(name, args string) *Completion {
	return &Completion{FinishReason: "tool_calls", Message: Message{Role: "assistant",
		ToolCalls: []ToolCall{{ID: "call_" + name, Type: "function", Function: FunctionCall{Name: name, Arguments: args}}}}}
}

func (Scripted) say(onDelta func(string), text string) *Completion {
	if onDelta != nil {
		r := []rune(text)
		for i := 0; i < len(r); i += 12 {
			j := min(i+12, len(r))
			onDelta(string(r[i:j]))
		}
	}
	return &Completion{FinishReason: "stop", Message: Message{Role: "assistant", Content: text}}
}
