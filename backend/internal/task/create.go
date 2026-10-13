package task

import (
	"context"
	"fmt"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/expr"
)

// MapRow converts a source row (SQL row or push payload) into snapshot
// fields according to the config's field mapping.
func MapRow(spec *ConfigSpec, row map[string]any) (expr.Vars, []string) {
	v := expr.Vars{}
	var missing []string
	for _, f := range spec.Fields {
		raw, ok := row[f.Column]
		if !ok {
			raw, ok = row[f.Field]
		}
		if !ok || raw == nil || expr.ToString(raw) == "" {
			if f.Required {
				missing = append(missing, f.Field)
			}
			continue
		}
		switch f.Type {
		case "datetime":
			if t, ok := expr.ToTime(raw); ok {
				v[f.Field] = t.Format(time.RFC3339)
			} else {
				missing = append(missing, f.Field)
			}
		case "number":
			if n, ok := expr.ToFloat(raw); ok {
				v[f.Field] = n
			} else {
				v[f.Field] = expr.ToString(raw)
			}
		case "bool":
			v[f.Field] = expr.Truthy(raw)
		default:
			v[f.Field] = expr.ToString(raw)
		}
	}
	return v, missing
}

// Candidate is one evaluated source row.
type Candidate struct {
	Vars      expr.Vars `json:"fields"`
	Missing   []string  `json:"missing,omitempty"`
	Eligible  bool      `json:"eligible"`
	Matched   []string  `json:"matched,omitempty"`
	DedupeKey string    `json:"dedupe_key"`
	Exists    bool      `json:"exists"`
	Error     string    `json:"error,omitempty"`
}

func (s *Service) evaluate(ctx context.Context, tt *TaskType, spec *ConfigSpec, row map[string]any, shadow bool) Candidate {
	v, missing := MapRow(spec, row)
	c := Candidate{Vars: v, Missing: missing}
	if len(missing) > 0 {
		c.Error = fmt.Sprintf("缺少必填字段 %v", missing)
		return c
	}
	ok, matched, err := spec.Eligibility.Eval(v, s.now())
	if err != nil {
		c.Error = err.Error()
		return c
	}
	c.Eligible, c.Matched = ok, matched
	key, err := tt.DedupeKey(v, shadow)
	if err != nil {
		c.Error = err.Error()
		c.Eligible = false
		return c
	}
	c.DedupeKey = key
	var n int64
	_ = s.conn.QueryRowCtx(ctx, &n, "SELECT COUNT(1) FROM bw_task WHERE type_key=? AND dedupe_key=?", tt.TypeKey, key)
	c.Exists = n > 0
	return c
}

// createTask inserts the task (identity = type + dedupe key), locks both
// version numbers, stores the snapshot and starts the run. Returns 0 when the
// dedupe key already exists.
func (s *Service) createTask(ctx context.Context, tt *TaskType, cfgVersion int64, c Candidate, origin Origin) (int64, error) {
	if tt.ActiveFlowVersion == 0 || cfgVersion == 0 {
		return 0, fmt.Errorf("任务类型 %s 尚未发布配置和流程", tt.TypeKey)
	}
	shadow := 0
	if tt.Status == TypeShadow {
		shadow = 1
	}
	objectKey := ""
	if fs := tt.DedupeFields(); len(fs) > 0 {
		objectKey = c.Vars.String(fs[0])
	}
	var taskID, runID int64
	err := s.conn.TransactCtx(ctx, func(ctx context.Context, q sqlx.Session) error {
		now := s.nowMs()
		id, err := db.Insert(ctx, q, `INSERT INTO bw_task (type_key, dedupe_key, object_key, is_shadow, config_version, flow_version, title, team, snapshot, origin, status, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			tt.TypeKey, c.DedupeKey, objectKey, shadow, cfgVersion, tt.ActiveFlowVersion, tt.Name, tt.Team, db.JSON(c.Vars), db.JSON(origin), StatusOpen, now, now)
		if err != nil {
			return err
		}
		vars := c.Vars.Clone()
		vars["task.id"] = id
		vars["task.type"] = tt.TypeKey
		rid, err := s.eng.StartRun(ctx, q, tt.FlowKey(), tt.ActiveFlowVersion, id, vars)
		if err != nil {
			return err
		}
		if _, err := q.ExecCtx(ctx, "UPDATE bw_task SET run_id=? WHERE id=?", rid, id); err != nil {
			return err
		}
		src := map[string]string{"scan": "扫描命中", "push": "业务 Push", "manual": "管理员补建", "backfill": "回扫"}[origin.Source]
		shadowNote := ""
		if shadow == 1 {
			shadowNote = "（影子运行：不推送）"
		}
		s.log(ctx, q, id, "create", origin.Actor, "%s · 配置 v%d · 流程 v%d%s", src, cfgVersion, tt.ActiveFlowVersion, shadowNote)
		taskID, runID = id, rid
		return nil
	})
	if db.IsDuplicate(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := s.eng.Advance(ctx, runID); err != nil && err != engine.ErrBusy {
		logx.WithContext(ctx).Errorf("advance run %d: %v", runID, err)
	}
	return taskID, nil
}

// ManualCreate lets an admin backfill a missed task. Eligibility is reported
// but not enforced; dedupe still applies.
func (s *Service) ManualCreate(ctx context.Context, actor, typeKey string, fields map[string]any, note string) (int64, *Candidate, error) {
	tt, err := s.Type(ctx, s.conn, typeKey)
	if err != nil {
		return 0, nil, err
	}
	if tt.Status != TypeEnabled && tt.Status != TypeShadow {
		return 0, nil, BadRequest("任务类型未启用")
	}
	cfg, err := s.Config(ctx, s.conn, typeKey, tt.ActiveConfigVersion)
	if err != nil {
		return 0, nil, err
	}
	c := s.evaluate(ctx, tt, cfg.Spec, fields, tt.Status == TypeShadow)
	if c.DedupeKey == "" {
		return 0, &c, BadRequest("无法创建：%s", c.Error)
	}
	if c.Exists {
		return 0, &c, Conflict(c, "该对象已有任务（去重键 %s）", c.DedupeKey)
	}
	id, err := s.createTask(ctx, tt, cfg.Version, c, Origin{Source: "manual", At: s.nowMs(), ConfigVersion: cfg.Version, Matched: c.Matched, Actor: actor})
	if err == nil {
		s.Audit(ctx, s.conn, actor, "manual_create", typeKey, map[string]any{"task_id": id, "fields": fields, "note": note})
	}
	return id, &c, err
}
