// Package expr evaluates the small, structured expression language used by
// configs: {{field}} templates, time expressions ("case.created_at + 3d") and
// rule-based conditions. It is deliberately not Turing complete so that
// configs can be statically checked for the fields they reference.
package expr

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Vars is a flat field map: "case.appeal_id" -> value.
type Vars map[string]any

func (v Vars) Clone() Vars {
	out := make(Vars, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

func (v Vars) Merge(o map[string]any) Vars {
	for k, x := range o {
		v[k] = x
	}
	return v
}

func (v Vars) Get(path string) (any, bool) {
	x, ok := v[strings.TrimSpace(path)]
	return x, ok
}

func (v Vars) String(path string) string {
	x, ok := v.Get(path)
	if !ok || x == nil {
		return ""
	}
	return ToString(x)
}

// ---------- templates ----------

var tplRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// Render replaces {{path}} with values; missing values render as "".
func Render(tpl string, vars Vars) string {
	return tplRe.ReplaceAllStringFunc(tpl, func(m string) string {
		return vars.String(tplRe.FindStringSubmatch(m)[1])
	})
}

// TemplateFields lists the fields a template references.
func TemplateFields(tpl string) []string {
	var out []string
	for _, m := range tplRe.FindAllStringSubmatch(tpl, -1) {
		out = append(out, m[1])
	}
	return out
}

// ---------- values ----------

func ToString(x any) string {
	switch t := x.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func ToFloat(x any) (float64, bool) {
	switch t := x.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// ToTime accepts RFC3339 / "2006-01-02 15:04:05" (UTC) strings and unix millis.
func ToTime(x any) (time.Time, bool) {
	switch t := x.(type) {
	case time.Time:
		return t.UTC(), true
	case string:
		s := strings.TrimSpace(t)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if tm, err := time.Parse(layout, s); err == nil {
				return tm.UTC(), true
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return time.UnixMilli(n).UTC(), true
		}
	default:
		if f, ok := ToFloat(x); ok && f > 0 {
			return time.UnixMilli(int64(f)).UTC(), true
		}
	}
	return time.Time{}, false
}

func Truthy(x any) bool {
	switch t := x.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s != "" && s != "0" && s != "false" && s != "no"
	default:
		f, ok := ToFloat(x)
		return ok && f != 0
	}
}

// ---------- durations & time expressions ----------

var durRe = regexp.MustCompile(`^(\d+)(d|h|m|s)`)

// ParseDuration parses "3d", "48h", "1d12h", "30m".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	for s != "" {
		m := durRe.FindStringSubmatch(s)
		if m == nil {
			return 0, fmt.Errorf("invalid duration %q (use e.g. 3d, 48h, 30m)", s)
		}
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			total += time.Duration(n) * 24 * time.Hour
		case "h":
			total += time.Duration(n) * time.Hour
		case "m":
			total += time.Duration(n) * time.Minute
		case "s":
			total += time.Duration(n) * time.Second
		}
		s = s[len(m[0]):]
	}
	return total, nil
}

var timeExprRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*(?:([+-])\s*([0-9dhms]+))?\s*$`)

// TimeExpr is "<field|now> [+|- duration]" or an absolute timestamp.
type TimeExpr string

func (e TimeExpr) Fields() []string {
	m := timeExprRe.FindStringSubmatch(string(e))
	if m == nil || m[1] == "now" {
		return nil
	}
	return []string{m[1]}
}

func (e TimeExpr) Validate() error {
	if _, ok := ToTime(string(e)); ok {
		return nil
	}
	m := timeExprRe.FindStringSubmatch(string(e))
	if m == nil {
		return fmt.Errorf("invalid time expression %q (use e.g. case.created_at + 3d)", string(e))
	}
	if m[3] != "" {
		if _, err := ParseDuration(m[3]); err != nil {
			return err
		}
	}
	return nil
}

func (e TimeExpr) Eval(vars Vars, now time.Time) (time.Time, error) {
	if t, ok := ToTime(string(e)); ok {
		return t, nil
	}
	m := timeExprRe.FindStringSubmatch(string(e))
	if m == nil {
		return time.Time{}, fmt.Errorf("invalid time expression %q", string(e))
	}
	var base time.Time
	if m[1] == "now" {
		base = now
	} else {
		v, ok := vars.Get(m[1])
		if !ok {
			return time.Time{}, fmt.Errorf("time field %s missing", m[1])
		}
		t, ok := ToTime(v)
		if !ok {
			return time.Time{}, fmt.Errorf("field %s is not a time: %v", m[1], v)
		}
		base = t
	}
	if m[3] != "" {
		d, err := ParseDuration(m[3])
		if err != nil {
			return time.Time{}, err
		}
		if m[2] == "-" {
			d = -d
		}
		base = base.Add(d)
	}
	return base.UTC(), nil
}

// ---------- conditions ----------

// Rule is one comparison. Ops: eq ne in not_in gt gte lt lte exists not_exists
// contains is_true is_false elapsed_gte elapsed_lt (now - time field vs duration).
type Rule struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
	Label string `json:"label,omitempty"`
}

// Group: all rules in All must match, and if Any is non-empty at least one of them.
type Group struct {
	All []Rule `json:"all,omitempty"`
	Any []Rule `json:"any,omitempty"`
}

var Ops = []string{"eq", "ne", "in", "not_in", "gt", "gte", "lt", "lte", "exists", "not_exists", "contains", "is_true", "is_false", "elapsed_gte", "elapsed_lt"}

func (g Group) Empty() bool { return len(g.All) == 0 && len(g.Any) == 0 }

func (g Group) Fields() []string {
	var out []string
	for _, r := range append(append([]Rule{}, g.All...), g.Any...) {
		out = append(out, r.Field)
	}
	return out
}

func (g Group) Validate() []string {
	var errs []string
	for _, r := range append(append([]Rule{}, g.All...), g.Any...) {
		if strings.TrimSpace(r.Field) == "" {
			errs = append(errs, "condition field is empty")
		}
		known := false
		for _, op := range Ops {
			if op == r.Op {
				known = true
			}
		}
		if !known {
			errs = append(errs, fmt.Sprintf("unknown operator %q on %s", r.Op, r.Field))
		}
		if r.Op == "elapsed_gte" || r.Op == "elapsed_lt" {
			if _, err := ParseDuration(ToString(r.Value)); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", r.Field, err))
			}
		}
	}
	return errs
}

// Eval returns whether the group matches, and the labels of matched rules.
func (g Group) Eval(vars Vars, now time.Time) (bool, []string, error) {
	var matched []string
	for _, r := range g.All {
		ok, err := r.Eval(vars, now)
		if err != nil {
			return false, nil, err
		}
		if !ok {
			return false, nil, nil
		}
		matched = append(matched, r.Describe())
	}
	if len(g.Any) > 0 {
		hit := false
		for _, r := range g.Any {
			ok, err := r.Eval(vars, now)
			if err != nil {
				return false, nil, err
			}
			if ok {
				hit = true
				matched = append(matched, r.Describe())
			}
		}
		if !hit {
			return false, nil, nil
		}
	}
	return true, matched, nil
}

func (r Rule) Describe() string {
	if r.Label != "" {
		return r.Label
	}
	if r.Value == nil {
		return fmt.Sprintf("%s %s", r.Field, r.Op)
	}
	return fmt.Sprintf("%s %s %s", r.Field, r.Op, ToString(r.Value))
}

func (r Rule) Eval(vars Vars, now time.Time) (bool, error) {
	v, has := vars.Get(r.Field)
	switch r.Op {
	case "exists":
		return has && v != nil && ToString(v) != "", nil
	case "not_exists":
		return !has || v == nil || ToString(v) == "", nil
	case "is_true":
		return Truthy(v), nil
	case "is_false":
		return !Truthy(v), nil
	case "eq":
		return equal(v, r.Value), nil
	case "ne":
		return !equal(v, r.Value), nil
	case "in", "not_in":
		hit := false
		for _, c := range toList(r.Value) {
			if equal(v, c) {
				hit = true
				break
			}
		}
		if r.Op == "in" {
			return hit, nil
		}
		return !hit, nil
	case "contains":
		return strings.Contains(ToString(v), ToString(r.Value)), nil
	case "gt", "gte", "lt", "lte":
		a, ok1 := ToFloat(v)
		b, ok2 := ToFloat(r.Value)
		if !ok1 || !ok2 {
			ta, ok3 := ToTime(v)
			tb, ok4 := ToTime(r.Value)
			if !ok3 || !ok4 {
				return false, nil
			}
			a, b = float64(ta.UnixMilli()), float64(tb.UnixMilli())
		}
		switch r.Op {
		case "gt":
			return a > b, nil
		case "gte":
			return a >= b, nil
		case "lt":
			return a < b, nil
		default:
			return a <= b, nil
		}
	case "elapsed_gte", "elapsed_lt":
		t, ok := ToTime(v)
		if !ok {
			return false, nil
		}
		d, err := ParseDuration(ToString(r.Value))
		if err != nil {
			return false, err
		}
		el := now.Sub(t)
		if r.Op == "elapsed_gte" {
			return el >= d, nil
		}
		return el < d, nil
	}
	return false, fmt.Errorf("unknown operator %q", r.Op)
}

func equal(a, b any) bool {
	fa, ok1 := ToFloat(a)
	fb, ok2 := ToFloat(b)
	if ok1 && ok2 {
		return fa == fb
	}
	return ToString(a) == ToString(b)
}

func toList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case string:
		var out []any
		for _, s := range strings.Split(t, ",") {
			out = append(out, strings.TrimSpace(s))
		}
		return out
	}
	return []any{v}
}

// Uniq returns sorted unique non-empty strings.
func Uniq(in []string) []string {
	m := map[string]bool{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
