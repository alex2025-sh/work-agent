package datasrc

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"bywork/internal/db"
	"bywork/internal/expr"
)

// ---------- read-only SQL sources (数仓 SQL trigger) ----------

// Sources holds named read-only datasources. In production each entry should
// point at a read-only account or replica; the guard in Compile is a second
// line of defence, not the only one.
type Sources struct {
	dbs     map[string]*sql.DB
	timeout time.Duration
	maxRows int
}

func NewSources(timeout time.Duration, maxRows int) *Sources {
	return &Sources{dbs: map[string]*sql.DB{}, timeout: timeout, maxRows: maxRows}
}

func (s *Sources) Add(name string, d *sql.DB) { s.dbs[name] = d }

func (s *Sources) Names() []string {
	var out []string
	for k := range s.dbs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type Result struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// Query compiles and runs a read-only query with bound parameters.
func (s *Sources) Query(ctx context.Context, source, query string, vars expr.Vars, limit int) (*Result, error) {
	d, ok := s.dbs[source]
	if !ok {
		return nil, fmt.Errorf("unknown datasource %q", source)
	}
	q, args, err := Compile(query, vars)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > s.maxRows {
		limit = s.maxRows
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols}
	for rows.Next() {
		if len(res.Rows) >= limit {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				m[c] = string(v)
			case time.Time:
				m[c] = v.UTC().Format(time.RFC3339)
			default:
				m[c] = v
			}
		}
		res.Rows = append(res.Rows, m)
	}
	return res, rows.Err()
}

// ---------- data.* capabilities (取数节点 / 实时读取) ----------

type Capability struct {
	Name    string                                                                                  `json:"name"`
	Title   string                                                                                  `json:"title"`
	Desc    string                                                                                  `json:"desc"`
	Args    []string                                                                                `json:"args"`
	Outputs []string                                                                                `json:"outputs"`
	Call    func(ctx context.Context, q db.Querier, args map[string]string) (map[string]any, error) `json:"-"`
}

type Registry struct{ caps map[string]*Capability }

func NewRegistry() *Registry { return &Registry{caps: map[string]*Capability{}} }

func (r *Registry) Register(c *Capability) { r.caps[c.Name] = c }

func (r *Registry) Get(name string) (*Capability, bool) {
	c, ok := r.caps[name]
	return c, ok
}

func (r *Registry) List() []*Capability {
	out := []*Capability{}
	for _, c := range r.caps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ErrNotFound marks a business object that no longer exists.
var ErrNotFound = fmt.Errorf("object not found")

// RegisterDemo installs capabilities backed by the demo_case / demo_vip
// tables. Production deployments replace these with CRM / VIP service adapters
// exposing the same names and outputs.
func RegisterDemo(r *Registry) {
	r.Register(&Capability{
		Name: "data.case.get", Title: "读取 Case", Desc: "实时读取 CRM Case 状态与阶段",
		Args:    []string{"appeal_id"},
		Outputs: []string{"case.status", "case.stage", "case.stage_entered_at", "case.case_type", "case.uid", "case.maker_review_entry_id", "case.created_at"},
		Call: func(ctx context.Context, q db.Querier, args map[string]string) (map[string]any, error) {
			var c struct {
				AppealID           string `db:"appeal_id"`
				UID                string `db:"uid"`
				CaseType           string `db:"case_type"`
				Status             string `db:"status"`
				Stage              string `db:"stage"`
				StageEnteredAt     int64  `db:"stage_entered_at"`
				MakerReviewEntryID string `db:"maker_review_entry_id"`
				CreatedAt          int64  `db:"created_at"`
			}
			err := q.QueryRowCtx(ctx, &c, "SELECT appeal_id, uid, case_type, status, stage, stage_entered_at, maker_review_entry_id, created_at FROM demo_case WHERE appeal_id = ?", args["appeal_id"])
			if db.IsNotFound(err) {
				return map[string]any{"case.status": "missing"}, nil
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"case.status": c.Status, "case.stage": c.Stage, "case.stage_entered_at": isoMs(c.StageEnteredAt),
				"case.case_type": c.CaseType, "case.uid": c.UID, "case.maker_review_entry_id": c.MakerReviewEntryID,
				"case.created_at": isoMs(c.CreatedAt),
			}, nil
		},
	})
	r.Register(&Capability{
		Name: "data.vip.profile", Title: "读取 VIP 档案", Desc: "VIP 等级、来源、近 6 个月盈利",
		Args:    []string{"uid"},
		Outputs: []string{"vip.is_vip", "vip.level", "vip.source", "vip.six_month_profit"},
		Call: func(ctx context.Context, q db.Querier, args map[string]string) (map[string]any, error) {
			var v struct {
				IsVip  int64  `db:"is_vip"`
				Level  string `db:"level"`
				Source string `db:"source"`
				Profit string `db:"six_month_profit"`
			}
			err := q.QueryRowCtx(ctx, &v, "SELECT is_vip, level, source, six_month_profit FROM demo_vip WHERE uid = ?", args["uid"])
			if db.IsNotFound(err) {
				return map[string]any{"vip.is_vip": false}, nil
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"vip.is_vip": v.IsVip == 1, "vip.level": v.Level, "vip.source": v.Source, "vip.six_month_profit": v.Profit}, nil
		},
	})
}

func isoMs(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
