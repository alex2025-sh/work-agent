package task

import (
	"context"
	"encoding/json"
	"strings"

	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// CardTemplate is versioned with the workflow (wf_definition.cards) and
// referenced by human nodes. Button destinations (exits) belong to the flow;
// the card only decides how buttons look.
type CardTemplate struct {
	Key           string                 `json:"key"`
	Name          string                 `json:"name"`
	Title         string                 `json:"title"`
	Fields        []CardField            `json:"fields"`
	Explain       bool                   `json:"explain"`
	AI            CardAI                 `json:"ai"`
	Buttons       map[string]ButtonStyle `json:"buttons"`
	Links         []CardLink             `json:"links"`
	ResultText    string                 `json:"result_text"`
	WaitingText   string                 `json:"waiting_text"`
	RefreshedText string                 `json:"refreshed_text"`
	LarkMaxFields int                    `json:"lark_max_fields"`
}

type CardField struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Format  string `json:"format"` // text | mono | mask | money | datetime
	Lark    bool   `json:"lark"`
	List    bool   `json:"list"`
	Missing string `json:"missing"`
}

type CardAI struct {
	Enabled   bool   `json:"enabled"`
	PromptKey string `json:"prompt_key"`
	Version   string `json:"version"`
}

type ButtonStyle struct {
	Label        string `json:"label"`
	Style        string `json:"style"` // primary | default | danger
	Confirm      bool   `json:"confirm"`
	NoteRequired bool   `json:"note_required"`
}

type CardLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// RenderedCard is the card snapshot stored on each work item round.
type RenderedCard struct {
	Template   string     `json:"template"`
	Title      string     `json:"title"`
	RoundLabel string     `json:"round_label"`
	Round      int64      `json:"round"`
	Fields     []RField   `json:"fields"`
	Buttons    []RButton  `json:"buttons"`
	Links      []CardLink `json:"links"`
	Explain    bool       `json:"explain"`
	AI         CardAI     `json:"ai"`
	Previous   string     `json:"previous,omitempty"`
	LarkMax    int        `json:"lark_max"`
	Texts      CardTexts  `json:"texts"`
}

type CardTexts struct {
	Result    string `json:"result"`
	Waiting   string `json:"waiting"`
	Refreshed string `json:"refreshed"`
}

type RField struct {
	Field     string `json:"field"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	Format    string `json:"format"`
	Lark      bool   `json:"lark"`
	List      bool   `json:"list"`
	Sensitive bool   `json:"sensitive"`
	Missing   bool   `json:"missing"`
}

type RButton struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Style        string `json:"style"`
	Kind         string `json:"kind"` // complete | external | delay
	Confirm      bool   `json:"confirm"`
	NoteRequired bool   `json:"note_required"`
}

func cardTemplates(def *engine.Definition) map[string]CardTemplate {
	m := map[string]CardTemplate{}
	_ = json.Unmarshal(def.Cards, &m)
	for k, v := range m {
		v.Key = k
		m[k] = v
	}
	return m
}

func (s *Service) renderCard(tpl CardTemplate, cfg *HumanConfig, vars expr.Vars, round int64, buttons []HumanButton, sensitive map[string]bool) RenderedCard {
	v := vars.Clone()
	label := cfg.RoundLabel
	title := tpl.Title
	if cfg.Title != "" {
		title = cfg.Title
	}
	if round > 1 {
		if cfg.LaterLabel != "" {
			label = cfg.LaterLabel
		}
		if cfg.LaterTitle != "" {
			title = cfg.LaterTitle
		}
	}
	v["node.round_label"] = label
	v["node.round"] = round
	rc := RenderedCard{Template: tpl.Key, Title: expr.Render(title, v), RoundLabel: label, Round: round,
		Fields: []RField{}, Buttons: []RButton{}, Links: []CardLink{},
		Explain: tpl.Explain, AI: tpl.AI, LarkMax: tpl.LarkMaxFields,
		Texts: CardTexts{Result: tpl.ResultText, Waiting: tpl.WaitingText, Refreshed: tpl.RefreshedText}}
	if rc.LarkMax == 0 {
		rc.LarkMax = 4
	}
	for _, f := range tpl.Fields {
		val, ok := v.Get(f.Field)
		rf := RField{Field: f.Field, Label: f.Label, Format: f.Format, Lark: f.Lark, List: f.List, Sensitive: sensitive[f.Field]}
		if !ok || expr.ToString(val) == "" {
			rf.Missing = true
			rf.Value = f.Missing
			if rf.Value == "" {
				rf.Value = "—"
			}
		} else {
			rf.Value = s.formatValue(f.Format, val)
		}
		rc.Fields = append(rc.Fields, rf)
	}
	for _, b := range buttons {
		st := tpl.Buttons[b.Key]
		rb := RButton{Key: b.Key, Kind: b.Kind, Label: b.Label, Style: "default"}
		if st.Label != "" {
			rb.Label = st.Label
		}
		if st.Style != "" {
			rb.Style = st.Style
		}
		rb.Confirm, rb.NoteRequired = st.Confirm, st.NoteRequired
		rc.Buttons = append(rc.Buttons, rb)
	}
	for _, l := range tpl.Links {
		u := expr.Render(l.URL, v)
		if s.allowedURL(u) {
			rc.Links = append(rc.Links, CardLink{Label: l.Label, URL: u})
		}
	}
	return rc
}

func (s *Service) formatValue(format string, val any) string {
	switch format {
	case "datetime":
		if t, ok := expr.ToTime(val); ok {
			return t.In(s.tz).Format("2006-01-02 15:04")
		}
	case "money":
		if f, ok := expr.ToFloat(val); ok {
			return formatMoney(f)
		}
	}
	return expr.ToString(val)
}

func formatMoney(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	s := expr.ToString(float64(int64(f*100+0.5)) / 100)
	intPart, frac := s, ""
	if i := strings.Index(s, "."); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String() + frac
	if neg {
		out = "-" + out
	}
	return out
}

// MaskCard applies per-viewer masking: format=mask is always partially
// masked; sensitive fields are fully masked unless the viewer may see them.
func MaskCard(c RenderedCard, canSeeSensitive bool) RenderedCard {
	out := c
	out.Fields = make([]RField, len(c.Fields))
	for i, f := range c.Fields {
		if !f.Missing {
			if f.Format == "mask" {
				f.Value = partialMask(f.Value)
			}
			if f.Sensitive && !canSeeSensitive {
				f.Value = "••••"
			}
		}
		out.Fields[i] = f
	}
	return out
}

func partialMask(s string) string {
	r := []rune(s)
	if len(r) <= 6 {
		return s
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

func decodeCard(s string) RenderedCard {
	c := RenderedCard{Fields: []RField{}, Buttons: []RButton{}, Links: []CardLink{}}
	_ = json.Unmarshal([]byte(s), &c)
	if c.Fields == nil {
		c.Fields = []RField{}
	}
	if c.Buttons == nil {
		c.Buttons = []RButton{}
	}
	if c.Links == nil {
		c.Links = []CardLink{}
	}
	return c
}

// CardView is the current presentation of a task's single card, shared by the
// web workbench, Lark and chat: actionable while a work item is active,
// otherwise the last round with its result and what happens next.
type CardView struct {
	TaskID     int64        `json:"task_id"`
	Card       RenderedCard `json:"card"`
	CardRev    int64        `json:"card_rev"`
	WorkItemID int64        `json:"work_item_id"`
	State      string       `json:"state"` // actionable | waiting | closed | unassigned | empty
	Result     string       `json:"result,omitempty"`
	Waiting    string       `json:"waiting,omitempty"`
	Previous   string       `json:"previous,omitempty"`
	WebURL     string       `json:"web_url"`
}

func (s *Service) cardView(ctx context.Context, q db.Querier, t *Task) (*CardView, error) {
	items, err := s.workItems(ctx, q, t.ID)
	if err != nil {
		return nil, err
	}
	cv := &CardView{TaskID: t.ID, CardRev: t.CardRev, Card: decodeCard("{}"), WebURL: strings.TrimRight(s.opt.WebBaseURL, "/") + "/tasks/" + itoa(t.ID), State: "empty"}
	if len(items) == 0 {
		if t.Status == StatusClosed {
			cv.State = "closed"
			cv.Result = "任务已结束 · " + CloseReasonText[t.CloseReason]
		}
		return cv, nil
	}
	last := items[len(items)-1]
	cv.Card = decodeCard(last.CardJSON)
	cv.WorkItemID = last.ID
	// previous round summary ("上一轮 48h Closed · 10/08")
	for i := len(items) - 2; i >= 0; i-- {
		p := items[i]
		if p.Status == "done" || p.Status == "delayed" {
			pc := decodeCard(p.CardJSON)
			cv.Previous = strings.TrimSpace(pc.RoundLabel+" "+s.buttonLabel(pc, p.Action)) + " · " + s.fmtTime(p.DoneAt)
			break
		}
		if p.Status == "voided" {
			pc := decodeCard(p.CardJSON)
			cv.Previous = strings.TrimSpace(pc.RoundLabel + " 未处理")
			break
		}
	}
	vars := t.Snapshot()
	switch {
	case last.Status == "active" && last.Assignee == "":
		cv.State = "unassigned"
	case last.Status == "active":
		cv.State = "actionable"
	case t.Status == StatusClosed:
		cv.State = "closed"
	default:
		cv.State = "waiting"
	}
	if last.Status != "active" && last.Action != "" {
		vars["action.label"] = s.buttonLabel(cv.Card, last.Action)
		vars["actor"] = s.memberName(ctx, q, last.ActionBy)
		vars["time"] = s.fmtTime(last.DoneAt)
		tpl := cv.Card.Texts.Result
		if tpl == "" {
			tpl = "已{{action.label}} · {{actor}} · {{time}}"
		}
		cv.Result = expr.Render(tpl, vars)
	}
	if t.Status == StatusClosed {
		reason := "任务已结束 · " + CloseReasonText[t.CloseReason]
		if cv.Result == "" {
			cv.Result = reason
		} else if t.CloseReason != CloseCompleted {
			cv.Result += " · " + reason
		}
	} else if cv.State == "waiting" && t.NextActionAt > 0 {
		vars["next.time"] = s.fmtTime(t.NextActionAt)
		tpl := cv.Card.Texts.Waiting
		if tpl == "" {
			tpl = "{{next.time}} 会复查并在这张卡上提醒"
		}
		cv.Waiting = expr.Render(tpl, vars)
	}
	return cv, nil
}

func (s *Service) buttonLabel(c RenderedCard, key string) string {
	for _, b := range c.Buttons {
		if b.Key == key {
			return b.Label
		}
	}
	return key
}

func itoa(n int64) string { return expr.ToString(n) }
