package ai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"bywork/internal/ai"
	"bywork/internal/clock"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/seed"
	"bywork/internal/task"
)

const typeKey = "VIP_ONGOING_REVIEW"

var (
	cfgUser = task.Viewer{UserID: "cfg_c", Role: "config", Team: "VIP_RM"}
	rmUser  = task.Viewer{UserID: "rm_li", Role: "rm", Team: "VIP_RM"}
)

type env struct {
	ctx   context.Context
	tasks *task.Service
	ai    *ai.Service
}

func newEnv(t *testing.T, llm ai.LLM, cfg ai.Config) *env {
	t.Helper()
	logx.Disable()
	raw, conn := db.OpenForTest(t)
	clk := clock.NewFixed(time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC))
	sources := datasrc.NewSources(5*time.Second, 5000)
	sources.Add("demo", raw)
	caps := datasrc.NewRegistry()
	datasrc.RegisterDemo(caps)
	eng := engine.New(conn, clk, "test")
	ts := task.New(conn, eng, caps, sources, clk, task.NewLogSender(), task.Options{Owner: "test", LinkDomains: []string{"lark.example.com"},
		SignKey: "k", WebBaseURL: "http://x", DisplayTimezone: "UTC", ScanBudget: 10 * time.Second, OutboxMaxTry: 3})
	ctx := context.Background()
	if err := seed.Run(ctx, conn, ts, clk.Now()); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if cfg.AllowedTables == nil {
		cfg.AllowedTables = []string{"demo_case", "demo_vip"}
	}
	return &env{ctx: ctx, tasks: ts, ai: ai.New(conn, ts, llm, clk, cfg)}
}

func (e *env) send(t *testing.T, sid, text string) []ai.Event {
	t.Helper()
	var evs []ai.Event
	var mu sync.Mutex
	if err := e.ai.Send(e.ctx, cfgUser, sid, text, func(ev ai.Event) { mu.Lock(); evs = append(evs, ev); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func proposals(evs []ai.Event) []*ai.Proposal {
	var out []*ai.Proposal
	for _, ev := range evs {
		if ev.Type == "proposal" {
			out = append(out, ev.Proposal)
		}
	}
	return out
}

func text(evs []ai.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Type == "delta" {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func TestScriptedConfigProposalAcceptAndStale(t *testing.T) {
	e := newEnv(t, ai.Scripted{}, ai.Config{Provider: "scripted"})
	ss, err := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	if err != nil {
		t.Fatal(err)
	}
	evs := e.send(t, ss.ID, "把 48 小时改成 72 小时")
	ps := proposals(evs)
	if len(ps) != 1 || ps[0].Kind != "config" || ps[0].OK != 1 {
		t.Fatalf("want one ok config proposal, got %+v\n%s", ps, text(evs))
	}
	if !strings.Contains(text(evs), "校验通过") {
		t.Fatalf("summary: %s", text(evs))
	}
	// nothing written yet
	before, _ := e.tasks.ConfigDraft(e.ctx, cfgUser.UserID, typeKey)
	if strings.Contains(mustJSON(before.Spec), `"72h"`) {
		t.Fatal("proposal must not touch the draft")
	}
	// an RM cannot accept
	if _, err := e.ai.Accept(e.ctx, rmUser, ps[0].ID); err == nil {
		t.Fatal("rm accepted a proposal")
	}
	p, err := e.ai.Accept(e.ctx, cfgUser, ps[0].ID)
	if err != nil || p.Status != "accepted" {
		t.Fatalf("accept: %v %+v", err, p)
	}
	after, _ := e.tasks.ConfigDraft(e.ctx, cfgUser.UserID, typeKey)
	if !strings.Contains(mustJSON(after.Spec), `"72h"`) || after.Revision == before.Revision {
		t.Fatalf("draft not updated: r%d %s", after.Revision, mustJSON(after.Spec.Eligibility))
	}
	if _, err := e.ai.Accept(e.ctx, cfgUser, ps[0].ID); err == nil {
		t.Fatal("accepted twice")
	}

	// a proposal made against an older revision goes stale
	evs = e.send(t, ss.ID, "把 72 小时改成 96 小时")
	p2 := proposals(evs)[0]
	if _, err := e.tasks.SaveConfigDraft(e.ctx, cfgUser.UserID, typeKey, *after.Spec, "手工修改"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ai.Accept(e.ctx, cfgUser, p2.ID); err == nil || !strings.Contains(err.Error(), "已被修改") {
		t.Fatalf("want stale conflict, got %v", err)
	}
	got, _ := e.ai.Proposal(e.ctx, cfgUser, p2.ID, false)
	if got.Status != "stale" {
		t.Fatalf("status %s", got.Status)
	}

	// history is persisted and other users cannot read it
	view, err := e.ai.SessionDetail(e.ctx, cfgUser, ss.ID)
	if err != nil || len(view.Proposals) != 2 || view.Session.Title == "" {
		t.Fatalf("detail: %v %+v", err, view)
	}
	if _, err := e.ai.SessionDetail(e.ctx, task.Viewer{UserID: "other", Role: "config"}, ss.ID); err == nil {
		t.Fatal("other user read the session")
	}
}

func TestScriptedFlowCardProposal(t *testing.T) {
	e := newEnv(t, ai.Scripted{}, ai.Config{Provider: "scripted"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	evs := e.send(t, ss.ID, "卡片加 case.stage（当前阶段）")
	ps := proposals(evs)
	if len(ps) != 1 || ps[0].Kind != "flow" || ps[0].OK != 1 {
		t.Fatalf("got %+v\n%s", ps, text(evs))
	}
	full, _ := e.ai.Proposal(e.ctx, cfgUser, ps[0].ID, true)
	var pl struct {
		Change ai.FlowChange `json:"change"`
	}
	_ = json.Unmarshal(full.Payload, &pl)
	if len(pl.Change.ChangedCards) != 1 || len(pl.Change.AddedNodes) != 0 {
		t.Fatalf("change: %+v", pl.Change)
	}
	if _, err := e.ai.Accept(e.ctx, cfgUser, ps[0].ID); err != nil {
		t.Fatal(err)
	}
	d, _ := e.tasks.FlowDraft(e.ctx, cfgUser.UserID, typeKey)
	if !strings.Contains(string(d.Cards), "case.stage\"") {
		t.Fatal("card field not in draft")
	}
}

func TestScriptedUnknownRequest(t *testing.T) {
	e := newEnv(t, ai.Scripted{}, ai.Config{Provider: "scripted"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	evs := e.send(t, ss.ID, "帮我优化一下")
	if len(proposals(evs)) != 0 || !strings.Contains(text(evs), "脚本模拟") {
		t.Fatalf("%s", text(evs))
	}
	if _, err := e.ai.CreateSession(e.ctx, rmUser, typeKey); err == nil {
		t.Fatal("rm opened an assistant session")
	}
}

// fakeOpenClaw mimics the Gateway's /v1/chat/completions streaming endpoint:
// it checks the request, streams a fragmented tool call, then answers.
type fakeOpenClaw struct {
	mu    sync.Mutex
	calls []map[string]any
	steps []func(w io.Writer, req map[string]any)
}

func (f *fakeOpenClaw) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer gw-token" {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if n >= len(f.steps) {
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"no more steps\"}}\n\n")
		return
	}
	f.steps[n](w, req)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func chunk(w io.Writer, delta string, finish string) {
	fin := "null"
	if finish != "" {
		fin = `"` + finish + `"`
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n", delta, fin)
}

func toolCallChunks(w io.Writer, id, name, args string) {
	chunk(w, fmt.Sprintf(`{"role":"assistant","tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":""}}]}`, id, name), "")
	for i := 0; i < len(args); i += 7 {
		j := min(i+7, len(args))
		b, _ := json.Marshal(args[i:j])
		chunk(w, fmt.Sprintf(`{"tool_calls":[{"index":0,"function":{"arguments":%s}}]}`, b), "")
	}
	chunk(w, `{}`, "tool_calls")
}

func TestOpenClawToolLoop(t *testing.T) {
	var spec json.RawMessage
	fake := &fakeOpenClaw{}
	fake.steps = []func(io.Writer, map[string]any){
		func(w io.Writer, req map[string]any) { toolCallChunks(w, "call_ctx", "get_context", "{}") },
		func(w io.Writer, req map[string]any) {
			// read the spec out of the get_context tool result, change the threshold
			msgs := req["messages"].([]any)
			last := msgs[len(msgs)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_ctx" {
				panic(fmt.Sprintf("want tool result, got %v", last))
			}
			var res struct {
				R struct {
					Config struct {
						Spec json.RawMessage `json:"spec"`
					} `json:"config"`
				} `json:"bywork_tool_result"`
			}
			_ = json.Unmarshal([]byte(last["content"].(string)), &res)
			spec = json.RawMessage(strings.Replace(string(res.R.Config.Spec), `"48h"`, `"60h"`, 1))
			args, _ := json.Marshal(map[string]any{"summary": "资格条件 48h → 60h", "spec": spec})
			toolCallChunks(w, "call_prop", "propose_config", string(args))
		},
		func(w io.Writer, req map[string]any) {
			chunk(w, `{"role":"assistant","content":"已生成提议，"}`, "")
			chunk(w, `{"content":"请查看 diff 后接受。"}`, "stop")
		},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	llm := &ai.OpenAICompat{BaseURL: srv.URL + "/v1", APIKey: "gw-token", Model: "openclaw/bywork"}
	e := newEnv(t, llm, ai.Config{Provider: "openclaw"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	evs := e.send(t, ss.ID, "资格门槛从 48 小时改成 60 小时")

	if got := text(evs); got != "已生成提议，请查看 diff 后接受。" {
		t.Fatalf("text %q", got)
	}
	ps := proposals(evs)
	if len(ps) != 1 || ps[0].OK != 1 {
		t.Fatalf("proposals %+v", ps)
	}
	if len(fake.calls) != 3 {
		t.Fatalf("calls %d", len(fake.calls))
	}
	first := fake.calls[0]
	if first["model"] != "openclaw/bywork" || first["stream"] != true || first["user"] != nil {
		t.Fatalf("request %v", first)
	}
	names := []string{}
	for _, tl := range first["tools"].([]any) {
		names = append(names, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "get_context,list_node_types,list_capabilities,describe_table,sample_query,propose_config,propose_flow" {
		t.Fatalf("tools %v", names)
	}
	// the follow-up call carries the assistant tool call and its result
	msgs := fake.calls[2]["messages"].([]any)
	asst := msgs[len(msgs)-2].(map[string]any)
	if asst["role"] != "assistant" || asst["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_prop" {
		t.Fatalf("assistant msg %v", asst)
	}

	// next turn resends the earlier turn's user/assistant text (stateless)
	fake.steps = append(fake.steps, func(w io.Writer, req map[string]any) {
		chunk(w, `{"role":"assistant","content":"好的"}`, "stop")
	})
	e.send(t, ss.ID, "谢谢")
	msgs = fake.calls[3]["messages"].([]any)
	if len(msgs) != 4 || msgs[1].(map[string]any)["content"] != "资格门槛从 48 小时改成 60 小时" {
		t.Fatalf("history %v", msgs)
	}
}

func TestOpenClawError(t *testing.T) {
	srv := httptest.NewServer(&fakeOpenClaw{})
	defer srv.Close()
	e := newEnv(t, &ai.OpenAICompat{BaseURL: srv.URL + "/v1", APIKey: "wrong"}, ai.Config{Provider: "openclaw"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	evs := e.send(t, ss.ID, "hi")
	if len(evs) == 0 || evs[len(evs)-1].Type != "error" || !strings.Contains(evs[len(evs)-1].Message, "401") {
		t.Fatalf("%+v", evs)
	}
	// the session is free for the next turn
	evs = e.send(t, ss.ID, "hi again")
	if evs[len(evs)-1].Type != "error" {
		t.Fatalf("%+v", evs)
	}
}

// toolLLM issues one fixed tool call, then stops; used to probe tool guards.
type toolLLM struct{ name, args string }

func (l toolLLM) Name() string { return "probe" }
func (l toolLLM) Chat(ctx context.Context, req ai.ChatRequest, onDelta func(string)) (*ai.Completion, error) {
	m := req.Messages[len(req.Messages)-1]
	if m.Role == "tool" {
		onDelta(m.Content)
		return &ai.Completion{Message: ai.Message{Role: "assistant", Content: m.Content}}, nil
	}
	return &ai.Completion{Message: ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "c1", Type: "function", Function: ai.FunctionCall{Name: l.name, Arguments: l.args}}}}}, nil
}

func TestSampleQueryMaskingAndAllowlist(t *testing.T) {
	q := `{"datasource":"demo","query":"SELECT c.appeal_id, c.uid, c.stage, v.six_month_profit FROM demo_case c JOIN demo_vip v ON v.uid=c.uid"}`
	e := newEnv(t, toolLLM{"sample_query", q}, ai.Config{Provider: "scripted"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	out := text(e.send(t, ss.ID, "看看数据"))
	if !strings.Contains(out, `"uid":"***"`) || !strings.Contains(out, `"six_month_profit":"***"`) || !strings.Contains(out, `"stage":"maker_review"`) {
		t.Fatalf("masking: %s", out)
	}
	if strings.Contains(out, "8812003390") {
		t.Fatal("uid leaked")
	}

	e2 := newEnv(t, toolLLM{"sample_query", `{"datasource":"demo","query":"SELECT * FROM bw_team_member"}`}, ai.Config{Provider: "scripted"})
	ss2, _ := e2.ai.CreateSession(e2.ctx, cfgUser, typeKey)
	if out := text(e2.send(t, ss2.ID, "x")); !strings.Contains(out, "白名单") {
		t.Fatalf("allowlist: %s", out)
	}

	e3 := newEnv(t, toolLLM{"sample_query", `{"datasource":"demo","query":"SELECT appeal_id FROM demo_case"}`}, ai.Config{Provider: "scripted", SampleRows: "none"})
	ss3, _ := e3.ai.CreateSession(e3.ctx, cfgUser, typeKey)
	if out := text(e3.send(t, ss3.ID, "x")); strings.Contains(out, `"rows"`) || !strings.Contains(out, `"row_count"`) {
		t.Fatalf("sample none: %s", out)
	}
}

func TestProposeFlowInvalid(t *testing.T) {
	ops := `{"summary":"加一个节点","ops":[{"op":"add_node","key":"notify2","type":"no_such_type"}]}`
	e := newEnv(t, toolLLM{"propose_flow", ops}, ai.Config{Provider: "scripted"})
	ss, _ := e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	out := text(e.send(t, ss.ID, "x"))
	if !strings.Contains(out, "未知节点类型") {
		t.Fatalf("%s", out)
	}
	// a dangling new node fails flow validation and cannot be accepted
	ops = `{"summary":"加一个结束节点","ops":[{"op":"add_node","key":"end_x","type":"end","name":"孤立","config":{"close_reason":"completed"}}]}`
	e = newEnv(t, toolLLM{"propose_flow", ops}, ai.Config{Provider: "scripted"})
	ss, _ = e.ai.CreateSession(e.ctx, cfgUser, typeKey)
	evs := e.send(t, ss.ID, "x")
	ps := proposals(evs)
	if len(ps) != 1 || ps[0].OK != 0 {
		t.Fatalf("%+v %s", ps, text(evs))
	}
	if _, err := e.ai.Accept(e.ctx, cfgUser, ps[0].ID); err == nil {
		t.Fatal("accepted invalid proposal")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
