package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"

	"bywork/internal/db"
)

// Sender delivers a card to Lark. Card updates are in place by message id.
type Sender interface {
	Send(ctx context.Context, openID string, card map[string]any) (messageID string, err error)
	Update(ctx context.Context, messageID string, card map[string]any) error
}

type cardPayload struct {
	TaskID int64    `json:"task_id"`
	OpenID string   `json:"open_id"`
	Rev    int64    `json:"rev"`
	View   CardView `json:"view"`
}

// enqueueCard writes the task's current card into the outbox, in the caller's
// transaction (design: 状态变更和待发消息同一事务写入 outbox).
func (s *Service) enqueueCard(ctx context.Context, q db.Querier, t *Task, tt *TaskType) error {
	if t.IsShadow == 1 {
		return nil // shadow: never pushed
	}
	if tt.Status == TypeDisabled {
		s.log(ctx, q, t.ID, "suppressed", "system", "任务类型已停用：只记录，不推送")
		return nil
	}
	if t.Assignee == "" {
		return nil
	}
	m, err := s.Member(ctx, q, t.Assignee)
	if err != nil || m.LarkOpenID == "" {
		if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET attention=? WHERE id=?", withFlag(t.Attention, AttnDeliveryFailed, true), t.ID); err != nil {
			return err
		}
		t.Attention = withFlag(t.Attention, AttnDeliveryFailed, true)
		s.log(ctx, q, t.ID, "delivery", "system", "找不到 %s 的 Lark 身份，Lark 投递失败；站内待办照常", t.Assignee)
		return nil
	}
	view, err := s.cardView(ctx, q, t)
	if err != nil {
		return err
	}
	p := cardPayload{TaskID: t.ID, OpenID: m.LarkOpenID, Rev: t.CardRev, View: *view}
	_, err = q.ExecCtx(ctx, "INSERT INTO outbox (event_key, channel, kind, task_id, target, payload, status, next_retry_at, created_at) VALUES (?,?,?,?,?,?,?,?,?)",
		fmt.Sprintf("card:%d:%d", t.ID, t.CardRev), "lark", "card", t.ID, m.LarkOpenID, db.JSON(p), "pending", 0, s.nowMs())
	if db.IsDuplicate(err) {
		return nil
	}
	if err == nil {
		s.kickOutbox()
	}
	return err
}

// syncCard bumps the card revision and re-delivers the current card view
// (result / waiting / closed states update the same card in place).
func (s *Service) syncCard(ctx context.Context, q db.Querier, taskID int64) error {
	if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET card_rev=card_rev+1 WHERE id=?", taskID); err != nil {
		return err
	}
	t, err := s.Task(ctx, q, taskID)
	if err != nil {
		return err
	}
	if _, err := q.ExecCtx(ctx, "UPDATE bw_work_item SET card_rev=? WHERE task_id=? AND status='active'", t.CardRev, taskID); err != nil {
		return err
	}
	tt, err := s.Type(ctx, q, t.TypeKey)
	if err != nil {
		return err
	}
	return s.enqueueCard(ctx, q, t, tt)
}

// ---------- outbox dispatch ----------

var outboxMu sync.Mutex

func (s *Service) kickOutbox() {
	if s.kick != nil {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// StartOutboxWorker sends right after commit; the outbox heartbeat retries.
func (s *Service) StartOutboxWorker(ctx context.Context) {
	s.kick = make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
				time.Sleep(20 * time.Millisecond) // let the writing tx commit
				if _, err := s.OutboxTick(ctx, 10*time.Second); err != nil {
					logx.Errorf("outbox: %v", err)
				}
			}
		}
	}()
}

type outboxRow struct {
	ID          int64  `db:"id"`
	EventKey    string `db:"event_key"`
	Channel     string `db:"channel"`
	Kind        string `db:"kind"`
	TaskID      int64  `db:"task_id"`
	Target      string `db:"target"`
	Payload     string `db:"payload"`
	Status      string `db:"status"`
	Attempts    int64  `db:"attempts"`
	NextRetryAt int64  `db:"next_retry_at"`
	LastError   string `db:"last_error"`
	CreatedAt   int64  `db:"created_at"`
	SentAt      int64  `db:"sent_at"`
}

// OutboxTick delivers pending messages; failures back off, and after
// OutboxMaxTry attempts the task is flagged 投递失败 for the leader.
func (s *Service) OutboxTick(ctx context.Context, budget time.Duration) (int, error) {
	if !outboxMu.TryLock() {
		return 0, nil
	}
	defer outboxMu.Unlock()
	deadline := time.Now().Add(budget)
	sent := 0
	for time.Now().Before(deadline) {
		var rows []outboxRow
		if err := s.conn.QueryRowsCtx(ctx, &rows, "SELECT * FROM outbox WHERE status='pending' AND next_retry_at <= ? ORDER BY id LIMIT 50", s.nowMs()); err != nil {
			return sent, err
		}
		if len(rows) == 0 {
			return sent, nil
		}
		for _, r := range rows {
			if time.Now().After(deadline) {
				break
			}
			if s.deliverOne(ctx, &r) {
				sent++
			}
		}
	}
	return sent, nil
}

func (s *Service) deliverOne(ctx context.Context, r *outboxRow) bool {
	// a newer card for the same task supersedes this one
	var newer int64
	_ = s.conn.QueryRowCtx(ctx, &newer, "SELECT COUNT(1) FROM outbox WHERE task_id=? AND kind='card' AND id>? AND status IN ('pending','sent')", r.TaskID, r.ID)
	if newer > 0 {
		_, _ = s.conn.ExecCtx(ctx, "UPDATE outbox SET status='superseded' WHERE id=?", r.ID)
		return false
	}
	var p cardPayload
	_ = json.Unmarshal([]byte(r.Payload), &p)
	t, err := s.Task(ctx, s.conn, r.TaskID)
	if err != nil {
		_, _ = s.conn.ExecCtx(ctx, "UPDATE outbox SET status='dead', last_error=? WHERE id=?", trunc(err.Error(), 1000), r.ID)
		return false
	}
	card := LarkCard(&p.View)
	var msgID string
	prevOpen, prevMsg, _ := strings.Cut(t.LarkMessageID, "|")
	if prevMsg != "" && prevOpen == p.OpenID {
		err = s.sender.Update(ctx, prevMsg, card)
		msgID = prevMsg
	} else {
		msgID, err = s.sender.Send(ctx, p.OpenID, card)
	}
	now := s.nowMs()
	if err == nil {
		_, _ = s.conn.ExecCtx(ctx, "UPDATE outbox SET status='sent', attempts=attempts+1, sent_at=?, last_error='' WHERE id=?", now, r.ID)
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_task SET lark_message_id=?, attention=? WHERE id=?", p.OpenID+"|"+msgID, withFlag(t.Attention, AttnDeliveryFailed, false), t.ID)
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_work_item SET delivered_at=? WHERE task_id=? AND status='active' AND delivered_at=0", now, t.ID)
		return true
	}
	attempts := r.Attempts + 1
	if attempts >= int64(s.opt.OutboxMaxTry) {
		_, _ = s.conn.ExecCtx(ctx, "UPDATE outbox SET status='dead', attempts=?, last_error=? WHERE id=?", attempts, trunc(err.Error(), 1000), r.ID)
		_, _ = s.conn.ExecCtx(ctx, "UPDATE bw_task SET attention=? WHERE id=?", withFlag(t.Attention, AttnDeliveryFailed, true), t.ID)
		s.log(ctx, s.conn, t.ID, "delivery", "system", "Lark 投递失败 %d 次，已停止重试：%s", attempts, err.Error())
		return false
	}
	backoff := time.Duration(30<<attempts) * time.Second
	_, _ = s.conn.ExecCtx(ctx, "UPDATE outbox SET attempts=?, next_retry_at=?, last_error=? WHERE id=?", attempts, now+backoff.Milliseconds(), trunc(err.Error(), 1000), r.ID)
	return false
}

// RetryDelivery re-queues the latest card (Leader「重试投递」).
func (s *Service) RetryDelivery(ctx context.Context, actor string, taskID int64) error {
	t, err := s.Task(ctx, s.conn, taskID)
	if err != nil {
		return err
	}
	if _, err := s.conn.ExecCtx(ctx, "UPDATE bw_task SET attention=? WHERE id=?", withFlag(t.Attention, AttnDeliveryFailed, false), t.ID); err != nil {
		return err
	}
	s.log(ctx, s.conn, t.ID, "ops", actor, "%s 重试投递", s.memberName(ctx, s.conn, actor))
	s.Audit(ctx, s.conn, actor, "retry_delivery", itoa(taskID), nil)
	return s.syncCard(ctx, s.conn, taskID)
}

// ---------- Lark card rendering ----------

// LarkCard renders a compact interactive card: top N fields, buttons, and a
// link back to Bywork. Button values carry the work item and card revision so
// stale clicks can be rejected server side.
func LarkCard(v *CardView) map[string]any {
	c := v.Card
	title := c.Title
	if title == "" {
		title = "Bywork 任务"
	}
	var fields []any
	n := 0
	for _, f := range MaskCard(c, true).Fields {
		if !f.Lark || n >= c.LarkMax {
			continue
		}
		n++
		fields = append(fields, map[string]any{"is_short": true, "text": map[string]any{"tag": "lark_md", "content": fmt.Sprintf("**%s**\n%s", f.Label, f.Value)}})
	}
	elements := []any{map[string]any{"tag": "div", "fields": fields}}
	if v.Previous != "" {
		elements = append(elements, map[string]any{"tag": "note", "elements": []any{map[string]any{"tag": "plain_text", "content": "上一轮 · " + v.Previous}}})
	}
	if v.State == "actionable" {
		var actions []any
		for _, b := range c.Buttons {
			typ := "default"
			if b.Style == "primary" {
				typ = "primary"
			} else if b.Style == "danger" {
				typ = "danger"
			}
			actions = append(actions, map[string]any{"tag": "button", "type": typ, "text": map[string]any{"tag": "plain_text", "content": b.Label},
				"value": map[string]any{"task_id": v.TaskID, "work_item_id": v.WorkItemID, "button": b.Key, "card_rev": v.CardRev}})
		}
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	} else {
		text := strings.TrimSpace(strings.Join(nonEmptyStrings(v.Result, v.Waiting), "\n"))
		if text != "" {
			elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": text}})
		}
	}
	elements = append(elements, map[string]any{"tag": "action", "actions": []any{map[string]any{"tag": "button", "type": "text",
		"text": map[string]any{"tag": "plain_text", "content": "在 Bywork 中查看详情与追问"}, "url": v.WebURL}}})
	template := "green"
	if v.State != "actionable" {
		template = "grey"
	}
	return map[string]any{
		"config":   map[string]any{"wide_screen_mode": true, "update_multi": true},
		"header":   map[string]any{"template": template, "title": map[string]any{"tag": "plain_text", "content": title}},
		"elements": elements,
	}
}

func nonEmptyStrings(in ...string) []string {
	out := []string{}
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- senders ----------

// LogSender keeps sent cards in memory (dev / demo). Open ids starting with
// "fail_" always fail, to exercise retries and 投递失败.
type LogSender struct {
	mu   sync.Mutex
	seq  int
	Msgs map[string]*SentCard
}

type SentCard struct {
	MessageID string         `json:"message_id"`
	OpenID    string         `json:"open_id"`
	Card      map[string]any `json:"card"`
	Updates   int            `json:"updates"`
	At        time.Time      `json:"at"`
}

func NewLogSender() *LogSender { return &LogSender{Msgs: map[string]*SentCard{}} }

func (l *LogSender) Send(_ context.Context, openID string, card map[string]any) (string, error) {
	if strings.HasPrefix(openID, "fail_") {
		return "", fmt.Errorf("lark: open_id %s not reachable", openID)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	id := fmt.Sprintf("om_dev_%d", l.seq)
	l.Msgs[id] = &SentCard{MessageID: id, OpenID: openID, Card: card, At: time.Now()}
	logx.Infof("lark(dev) send %s -> %s", id, openID)
	return id, nil
}

func (l *LogSender) Update(_ context.Context, id string, card map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.Msgs[id]
	if !ok {
		return fmt.Errorf("lark: message %s not found", id)
	}
	m.Card, m.Updates, m.At = card, m.Updates+1, time.Now()
	return nil
}

func (l *LogSender) List() []*SentCard {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*SentCard
	for _, m := range l.Msgs {
		out = append(out, m)
	}
	return out
}

// LarkSender talks to the Lark Open API (im/v1/messages).
type LarkSender struct {
	BaseURL, AppID, AppSecret string
	Client                    *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (l *LarkSender) tenantToken(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.token != "" && time.Now().Before(l.expiry) {
		return l.token, nil
	}
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := l.call(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": l.AppID, "app_secret": l.AppSecret}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", fmt.Errorf("lark token: %d %s", out.Code, out.Msg)
	}
	l.token, l.expiry = out.Token, time.Now().Add(time.Duration(out.Expire-60)*time.Second)
	return l.token, nil
}

func (l *LarkSender) call(ctx context.Context, method, path, token string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(l.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cli := l.Client
	if cli == nil {
		cli = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("lark %s %s: %d %s", method, path, resp.StatusCode, trunc(string(data), 300))
	}
	return json.Unmarshal(data, out)
}

func (l *LarkSender) Send(ctx context.Context, openID string, card map[string]any) (string, error) {
	tok, err := l.tenantToken(ctx)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(card)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := l.call(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", tok,
		map[string]string{"receive_id": openID, "msg_type": "interactive", "content": string(content)}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", fmt.Errorf("lark send: %d %s", out.Code, out.Msg)
	}
	return out.Data.MessageID, nil
}

func (l *LarkSender) Update(ctx context.Context, messageID string, card map[string]any) error {
	tok, err := l.tenantToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(card)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := l.call(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+messageID, tok, map[string]string{"content": string(content)}, &out); err != nil {
		return err
	}
	if out.Code != 0 {
		return fmt.Errorf("lark update: %d %s", out.Code, out.Msg)
	}
	return nil
}
