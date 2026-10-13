package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/db"
	"bywork/internal/expr"
)

// Engine schedules, persists and selects edges. It knows no concrete node.
type Engine struct {
	conn     sqlx.SqlConn
	clock    clock.Clock
	owner    string
	listener Listener

	mu    sync.RWMutex
	nodes map[string]NodeExecutor
	defs  map[string]*Definition // published, immutable
}

func New(conn sqlx.SqlConn, c clock.Clock, owner string) *Engine {
	return &Engine{conn: conn, clock: c, owner: owner, nodes: map[string]NodeExecutor{}, defs: map[string]*Definition{}}
}

func (e *Engine) SetListener(l Listener) { e.listener = l }
func (e *Engine) Now() time.Time         { return e.clock.Now() }
func (e *Engine) Conn() sqlx.SqlConn     { return e.conn }

// Register adds a node type. New node = schema + IO declaration + executor.
func (e *Engine) Register(x NodeExecutor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nodes[x.Meta().Type] = x
}

func (e *Engine) Executor(typ string) (NodeExecutor, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	x, ok := e.nodes[typ]
	return x, ok
}

func (e *Engine) NodeTypes() []NodeMeta {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := []NodeMeta{}
	for _, x := range e.nodes {
		out = append(out, x.Meta())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func (e *Engine) Definition(ctx context.Context, q db.Querier, flowKey string, version int64) (*Definition, error) {
	k := fmt.Sprintf("%s@%d", flowKey, version)
	e.mu.RLock()
	d, ok := e.defs[k]
	e.mu.RUnlock()
	if ok {
		return d, nil
	}
	d, err := LoadDefinition(ctx, q, flowKey, version)
	if err != nil {
		return nil, err
	}
	if version > 0 { // drafts (version 0) are mutable, never cache
		e.mu.Lock()
		e.defs[k] = d
		e.mu.Unlock()
	}
	return d, nil
}

var ErrBusy = errors.New("run is busy, try again")

// ---------- start ----------

// StartRun creates a run and its single token at the start node, inside the
// caller's transaction. Call Advance after commit.
func (e *Engine) StartRun(ctx context.Context, q db.Querier, flowKey string, version int64, taskID int64, vars expr.Vars) (int64, error) {
	def, err := e.Definition(ctx, q, flowKey, version)
	if err != nil {
		return 0, fmt.Errorf("load flow %s v%d: %w", flowKey, version, err)
	}
	start := def.Graph.Start()
	if start == nil {
		return 0, fmt.Errorf("flow %s v%d has no start node", flowKey, version)
	}
	now := clock.Ms(e.clock.Now())
	runID, err := db.Insert(ctx, q, "INSERT INTO wf_run (flow_key, flow_version, task_id, status, vars, result, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
		flowKey, version, taskID, "running", db.JSON(vars), "{}", now, now)
	if err != nil {
		return 0, err
	}
	if _, err := db.Insert(ctx, q, "INSERT INTO wf_token (run_id, node_key, status, seq, state, created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
		runID, start.Key, "ready", 1, "{}", now, now); err != nil {
		return 0, err
	}
	return runID, nil
}

// ---------- leasing ----------

func (e *Engine) lease(ctx context.Context, runID int64, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		now := clock.Ms(e.clock.Now())
		n, err := db.Affected(ctx, e.conn, "UPDATE wf_run SET lease_owner = ?, lease_until = ? WHERE id = ? AND (lease_until < ? OR lease_owner = ?)",
			e.owner, now+60_000, runID, now, e.owner)
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *Engine) release(ctx context.Context, runID int64) {
	_, _ = e.conn.ExecCtx(ctx, "UPDATE wf_run SET lease_owner = '', lease_until = 0 WHERE id = ? AND lease_owner = ?", runID, e.owner)
}

// ---------- advance ----------

// Advance executes ready steps until the token suspends or the run ends.
// Each step is checkpointed before and committed atomically after.
func (e *Engine) Advance(ctx context.Context, runID int64) error {
	ok, err := e.lease(ctx, runID, 2*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer e.release(ctx, runID)
	return e.advanceLocked(ctx, runID)
}

func (e *Engine) advanceLocked(ctx context.Context, runID int64) error {
	for i := 0; i < 200; i++ { // guard against accidental loops
		var tok Token
		err := e.conn.QueryRowCtx(ctx, &tok, "SELECT * FROM wf_token WHERE run_id = ? AND status = 'ready' ORDER BY id LIMIT 1", runID)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := e.step(ctx, runID, &tok, nil); err != nil {
			return err
		}
	}
	return fmt.Errorf("run %d exceeded step limit", runID)
}

// step runs one node (Execute, or Resume when ev != nil) for a token.
func (e *Engine) step(ctx context.Context, runID int64, tok *Token, ev *Event) error {
	var run Run
	if err := e.conn.QueryRowCtx(ctx, &run, "SELECT * FROM wf_run WHERE id = ?", runID); err != nil {
		return err
	}
	def, err := e.Definition(ctx, e.conn, run.FlowKey, run.FlowVersion)
	if err != nil {
		return err
	}
	node := def.Graph.Node(tok.NodeKey)
	if node == nil {
		return e.fail(ctx, &run, tok, fmt.Errorf("node %s not in flow", tok.NodeKey))
	}
	x, ok := e.Executor(node.Type)
	if !ok {
		return e.fail(ctx, &run, tok, fmt.Errorf("unknown node type %s", node.Type))
	}
	started := e.clock.Now()
	vars := run.Vars()
	// checkpoint before execution
	input := map[string]any{"config": json.RawMessage(orEmpty(string(node.Config)))}
	if ev != nil {
		input["event"] = ev
	}
	execID, err := db.Insert(ctx, e.conn, "INSERT INTO wf_node_exec (run_id, token_id, node_key, seq, status, input, output, started_at) VALUES (?,?,?,?,?,?,?,?)",
		runID, tok.ID, node.Key, tok.Seq, "running", db.JSON(input), "{}", clock.Ms(started))
	if err != nil {
		return err
	}

	return e.conn.TransactCtx(ctx, func(ctx context.Context, s sqlx.Session) error {
		in := &NodeInput{Q: s, Run: &run, Token: tok, Node: node, Def: def, Vars: vars, State: tok.State(), Now: started, Event: ev, Engine: e}
		var res NodeResult
		var xerr error
		if ev != nil {
			r, ok := x.(Resumer)
			if !ok {
				return nil
			}
			res, xerr = r.Resume(ctx, in)
		} else {
			res, xerr = x.Execute(ctx, in)
		}
		fin := e.clock.Now()
		if xerr != nil {
			_, _ = s.ExecCtx(ctx, "UPDATE wf_node_exec SET status='error', error=?, finished_at=?, duration_ms=? WHERE id=?",
				trunc(xerr.Error(), 1000), clock.Ms(fin), fin.Sub(started).Milliseconds(), execID)
			return e.suspendOnError(ctx, s, &run, tok, xerr)
		}
		if res.Suspend != nil && res.Suspend.Ignored {
			_, err := s.ExecCtx(ctx, "UPDATE wf_node_exec SET status='ignored', finished_at=?, duration_ms=? WHERE id=?", clock.Ms(fin), fin.Sub(started).Milliseconds(), execID)
			return err
		}
		return e.apply(ctx, s, &run, tok, def, node, vars, res, execID, started)
	})
}

func (e *Engine) apply(ctx context.Context, s sqlx.Session, run *Run, tok *Token, def *Definition, node *Node, vars expr.Vars, res NodeResult, execID int64, started time.Time) error {
	now := e.clock.Now()
	nowMs := clock.Ms(now)
	if len(res.Output) > 0 {
		vars.Merge(res.Output)
	}
	if _, err := s.ExecCtx(ctx, "UPDATE wf_run SET vars=?, updated_at=? WHERE id=?", db.JSON(vars), nowMs, run.ID); err != nil {
		return err
	}
	run.VarsJSON = db.JSON(vars)
	status := "ok"
	if res.Suspend != nil {
		status = "suspended"
	}
	if _, err := s.ExecCtx(ctx, "UPDATE wf_node_exec SET status=?, output=?, exit_name=?, finished_at=?, duration_ms=? WHERE id=?",
		status, db.JSON(res.Output), res.Exit, nowMs, now.Sub(started).Milliseconds(), execID); err != nil {
		return err
	}
	state := tok.State()
	if res.State != nil {
		state = res.State
	}

	switch {
	case res.Terminate != nil:
		return e.end(ctx, s, run, tok, "completed", res.Terminate)

	case res.Suspend != nil:
		for _, k := range res.Suspend.CancelTimers {
			if _, err := s.ExecCtx(ctx, "UPDATE wf_timer SET status='cancelled' WHERE token_id=? AND timer_key=? AND status='pending'", tok.ID, seqKey(tok.Seq, k)); err != nil {
				return err
			}
		}
		for _, t := range res.Suspend.Timers {
			if _, err := s.ExecCtx(ctx, "INSERT INTO wf_timer (run_id, token_id, token_seq, node_key, timer_key, due_at, status, created_at) VALUES (?,?,?,?,?,?,?,?)",
				run.ID, tok.ID, tok.Seq, node.Key, seqKey(tok.Seq, t.Key), clock.Ms(t.DueAt), "pending", nowMs); err != nil && !db.IsDuplicate(err) {
				return err
			}
		}
		if _, err := s.ExecCtx(ctx, "UPDATE wf_token SET status='waiting', state=?, wait_reason=?, updated_at=? WHERE id=?", db.JSON(state), res.Suspend.Reason, nowMs, tok.ID); err != nil {
			return err
		}
		if _, err := s.ExecCtx(ctx, "UPDATE wf_run SET status='waiting', updated_at=? WHERE id=?", nowMs, run.ID); err != nil {
			return err
		}
		if e.listener != nil {
			return e.listener.OnSuspend(ctx, s, run, node, res.Suspend.Timers)
		}
		return nil

	default:
		next, ok := def.Graph.Next(node.Key, res.Exit)
		if !ok {
			return e.suspendOnError(ctx, s, run, tok, fmt.Errorf("节点 %s 的出口 %q 没有去向", node.Key, res.Exit))
		}
		// leaving the node: its pending timers are void
		if _, err := s.ExecCtx(ctx, "UPDATE wf_timer SET status='cancelled' WHERE token_id=? AND token_seq=? AND status='pending'", tok.ID, tok.Seq); err != nil {
			return err
		}
		if _, err := s.ExecCtx(ctx, "UPDATE wf_token SET node_key=?, status='ready', seq=seq+1, state='{}', wait_reason='', updated_at=? WHERE id=?", next, nowMs, tok.ID); err != nil {
			return err
		}
		_, err := s.ExecCtx(ctx, "UPDATE wf_run SET status='running', updated_at=? WHERE id=?", nowMs, run.ID)
		return err
	}
}

func (e *Engine) end(ctx context.Context, s sqlx.Session, run *Run, tok *Token, status string, result map[string]any) error {
	nowMs := clock.Ms(e.clock.Now())
	if _, err := s.ExecCtx(ctx, "UPDATE wf_timer SET status='cancelled' WHERE run_id=? AND status='pending'", run.ID); err != nil {
		return err
	}
	if _, err := s.ExecCtx(ctx, "UPDATE wf_token SET status='done', updated_at=? WHERE run_id=?", nowMs, run.ID); err != nil {
		return err
	}
	if _, err := s.ExecCtx(ctx, "UPDATE wf_run SET status=?, result=?, updated_at=? WHERE id=?", status, db.JSON(result), nowMs, run.ID); err != nil {
		return err
	}
	if e.listener != nil {
		return e.listener.OnRunEnd(ctx, s, run, result)
	}
	return nil
}

func (e *Engine) suspendOnError(ctx context.Context, s sqlx.Session, run *Run, tok *Token, cause error) error {
	nowMs := clock.Ms(e.clock.Now())
	logx.WithContext(ctx).Errorf("run %d node %s suspended: %v", run.ID, tok.NodeKey, cause)
	if _, err := s.ExecCtx(ctx, "UPDATE wf_token SET status='suspended', wait_reason=?, updated_at=? WHERE id=?", trunc("error: "+cause.Error(), 250), nowMs, tok.ID); err != nil {
		return err
	}
	if _, err := s.ExecCtx(ctx, "UPDATE wf_run SET status='suspended', updated_at=? WHERE id=?", nowMs, run.ID); err != nil {
		return err
	}
	if e.listener != nil {
		return e.listener.OnRunError(ctx, s, run, tok.NodeKey, cause)
	}
	return nil
}

func (e *Engine) fail(ctx context.Context, run *Run, tok *Token, cause error) error {
	return e.conn.TransactCtx(ctx, func(ctx context.Context, s sqlx.Session) error {
		return e.suspendOnError(ctx, s, run, tok, cause)
	})
}

// ---------- events ----------

// EventResult is what a duplicate delivery of the same key gets back.
type EventResult struct {
	Duplicate bool           `json:"duplicate"`
	Stale     bool           `json:"stale"`
	Result    map[string]any `json:"result,omitempty"`
}

var ErrKeyConflict = errors.New("同一个幂等键对应了不同的参数")

// Deliver wakes a waiting token with an event. Idempotent on (run, ev.Key):
// same key + same payload returns the original result; a different payload is
// rejected.
func (e *Engine) Deliver(ctx context.Context, runID int64, ev Event, wait time.Duration) (*EventResult, error) {
	payload := db.JSON(ev)
	sum := sha256.Sum256([]byte(db.JSON(ev.Payload) + "|" + ev.NodeKey + "|" + ev.Kind))
	hash := hex.EncodeToString(sum[:])

	if prev, err := e.loadEvent(ctx, runID, ev.Key); err == nil {
		if prev.PayloadHash != hash {
			return nil, ErrKeyConflict
		}
		r := map[string]any{}
		_ = json.Unmarshal([]byte(prev.Result), &r)
		return &EventResult{Duplicate: true, Result: r}, nil
	}

	ok, err := e.lease(ctx, runID, wait)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBusy
	}
	defer e.release(ctx, runID)

	var tok Token
	err = e.conn.QueryRowCtx(ctx, &tok, "SELECT * FROM wf_token WHERE run_id = ? ORDER BY id LIMIT 1", runID)
	if err != nil {
		return nil, err
	}
	stale := tok.Status != "waiting" || tok.NodeKey != ev.NodeKey || (ev.TokenSeq > 0 && ev.TokenSeq != tok.Seq)
	nowMs := clock.Ms(e.clock.Now())
	if _, err := e.conn.ExecCtx(ctx, "INSERT INTO wf_event (run_id, event_key, kind, payload, payload_hash, result, created_at) VALUES (?,?,?,?,?,?,?)",
		runID, ev.Key, ev.Kind, payload, hash, db.JSON(map[string]any{"stale": stale}), nowMs); err != nil {
		if db.IsDuplicate(err) {
			return e.Deliver(ctx, runID, ev, 0)
		}
		return nil, err
	}
	if stale {
		return &EventResult{Stale: true}, nil
	}
	if err := e.step(ctx, runID, &tok, &ev); err != nil {
		// let the caller retry with the same key
		_, _ = e.conn.ExecCtx(ctx, "DELETE FROM wf_event WHERE run_id=? AND event_key=?", runID, ev.Key)
		return nil, err
	}
	if err := e.advanceLocked(ctx, runID); err != nil {
		return nil, err
	}
	return &EventResult{}, nil
}

type eventRow struct {
	ID          int64  `db:"id"`
	RunID       int64  `db:"run_id"`
	EventKey    string `db:"event_key"`
	Kind        string `db:"kind"`
	Payload     string `db:"payload"`
	PayloadHash string `db:"payload_hash"`
	Result      string `db:"result"`
	CreatedAt   int64  `db:"created_at"`
}

func (e *Engine) loadEvent(ctx context.Context, runID int64, key string) (*eventRow, error) {
	var r eventRow
	err := e.conn.QueryRowCtx(ctx, &r, "SELECT * FROM wf_event WHERE run_id = ? AND event_key = ?", runID, key)
	return &r, err
}

// Locked runs fn while holding the run's lease (used for terminate/reassign).
func (e *Engine) Locked(ctx context.Context, runID int64, fn func() error) error {
	ok, err := e.lease(ctx, runID, 3*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer e.release(ctx, runID)
	return fn()
}

// Retry re-executes a token suspended by an error (Leader "重新触发").
func (e *Engine) Retry(ctx context.Context, runID int64) error {
	ok, err := e.lease(ctx, runID, 2*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	defer e.release(ctx, runID)
	nowMs := clock.Ms(e.clock.Now())
	n, err := db.Affected(ctx, e.conn, "UPDATE wf_token SET status='ready', wait_reason='', updated_at=? WHERE run_id=? AND status='suspended'", nowMs, runID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("没有因错误挂起的执行点")
	}
	if _, err := e.conn.ExecCtx(ctx, "UPDATE wf_run SET status='running', updated_at=? WHERE id=?", nowMs, runID); err != nil {
		return err
	}
	return e.advanceLocked(ctx, runID)
}

// Terminate ends a run from outside (admin cancel, object invalidated, shadow end).
// It runs inside the caller's transaction; the caller must hold no lease conflict
// (terminate is safe to race: it only moves non-finished runs).
func (e *Engine) Terminate(ctx context.Context, s sqlx.Session, runID int64, result map[string]any) error {
	var run Run
	if err := s.QueryRowCtx(ctx, &run, "SELECT * FROM wf_run WHERE id = ?", runID); err != nil {
		return err
	}
	if run.Status == "completed" || run.Status == "terminated" {
		return nil
	}
	return e.end(ctx, s, &run, nil, "terminated", result)
}

// ---------- timers ----------

// TimerTick claims due timers and delivers them as events. Returns fired count.
func (e *Engine) TimerTick(ctx context.Context, budget time.Duration) (int, error) {
	deadline := time.Now().Add(budget)
	fired := 0
	for time.Now().Before(deadline) {
		now := clock.Ms(e.clock.Now())
		var due []Timer
		if err := e.conn.QueryRowsCtx(ctx, &due, "SELECT * FROM wf_timer WHERE status='pending' AND due_at <= ? AND lease_until < ? ORDER BY due_at LIMIT 50", now, now); err != nil {
			return fired, err
		}
		if len(due) == 0 {
			return fired, nil
		}
		progressed := false
		for _, t := range due {
			if time.Now().After(deadline) {
				break
			}
			n, err := db.Affected(ctx, e.conn, "UPDATE wf_timer SET lease_owner=?, lease_until=? WHERE id=? AND status='pending' AND lease_until < ?", e.owner, now+60_000, t.ID, now)
			if err != nil || n == 0 {
				continue
			}
			ev := Event{Kind: "timer", Key: fmt.Sprintf("timer:%d", t.ID), NodeKey: t.NodeKey, TokenSeq: t.TokenSeq, TimerKey: unseqKey(t.TimerKey)}
			if _, err := e.Deliver(ctx, t.RunID, ev, 0); err != nil {
				logx.WithContext(ctx).Errorf("timer %d deliver: %v", t.ID, err)
				continue // lease expires; retried by a later tick
			}
			_, _ = e.conn.ExecCtx(ctx, "UPDATE wf_timer SET status='fired' WHERE id=?", t.ID)
			fired++
			progressed = true
		}
		if !progressed {
			return fired, nil
		}
	}
	return fired, nil
}

func seqKey(seq int64, key string) string { return fmt.Sprintf("s%d:%s", seq, key) }

func unseqKey(k string) string {
	if i := strings.Index(k, ":"); i > 0 && strings.HasPrefix(k, "s") {
		return k[i+1:]
	}
	return k
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// PendingTimers lists pending timers of a run (for "接下来会发生什么").
func (e *Engine) PendingTimers(ctx context.Context, q db.Querier, runID int64) ([]Timer, error) {
	out := []Timer{}
	err := q.QueryRowsCtx(ctx, &out, "SELECT * FROM wf_timer WHERE run_id=? AND status='pending' ORDER BY due_at", runID)
	return out, err
}

// Executions lists node executions of a run.
func (e *Engine) Executions(ctx context.Context, runID int64) ([]NodeExec, error) {
	out := []NodeExec{}
	err := e.conn.QueryRowsCtx(ctx, &out, "SELECT * FROM wf_node_exec WHERE run_id=? ORDER BY id", runID)
	return out, err
}
