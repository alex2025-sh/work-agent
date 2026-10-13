package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/rest"

	"bywork/internal/datasrc"
	"bywork/internal/engine"
	"bywork/internal/expr"
	"bywork/internal/svc"
	"bywork/internal/task"
)

func RegisterHandlers(server *rest.Server, sc *svc.ServiceContext) {
	T := sc.Tasks
	auth := Auth(sc)
	routes := func(mw func(http.HandlerFunc) http.HandlerFunc, rs ...rest.Route) {
		for i := range rs {
			rs[i].Handler = mw(rs[i].Handler)
		}
		server.AddRoutes(rs)
	}
	requireRole := func(roles ...string) func(r *http.Request) error {
		return func(r *http.Request) error {
			v := viewer(r)
			for _, x := range roles {
				if v.Role == x {
					return nil
				}
			}
			return task.Forbidden("需要角色 %s", strings.Join(roles, "/"))
		}
	}
	configurer := requireRole("config", "admin")

	routes(auth,
		// ---------- identity ----------
		rest.Route{Method: http.MethodGet, Path: "/api/me", Handler: h(func(r *http.Request) (any, error) {
			v := viewer(r)
			m, err := T.Member(r.Context(), sc.Conn, v.UserID)
			return map[string]any{"user": m, "dev": sc.Config.Dev, "now": sc.Clock.Now().UnixMilli()}, err
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/members", Handler: h(func(r *http.Request) (any, error) {
			return T.Members(r.Context(), r.URL.Query().Get("team"))
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/members/:user/active", Handler: h(func(r *http.Request) (any, error) {
			var req struct{ Active bool }
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return nil, T.SetMemberActive(r.Context(), viewer(r), pathStr(r, "user"), req.Active)
		})},

		// ---------- RM workbench ----------
		rest.Route{Method: http.MethodGet, Path: "/api/my/tasks", Handler: h(func(r *http.Request) (any, error) {
			g := r.URL.Query().Get("group")
			if g == "" {
				g = "pending"
			}
			rows, counts, err := T.MyTasks(r.Context(), viewer(r), g)
			return map[string]any{"items": rows, "counts": counts}, err
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/tasks/:id", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return T.Detail(r.Context(), viewer(r), id)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/actions", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			var req task.ActionReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			res, err := T.Act(r.Context(), viewer(r), id, req)
			if err != nil {
				return nil, err
			}
			d, err := T.Detail(r.Context(), viewer(r), id)
			return map[string]any{"result": res, "detail": d}, err
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/open-link", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			var req task.OpenLinkReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.OpenLink(r.Context(), viewer(r), id, req)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/feedback", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			var req struct{ Kind, Note string }
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return nil, T.Feedback(r.Context(), viewer(r), id, req.Kind, req.Note)
		})},

		// ---------- leader ----------
		rest.Route{Method: http.MethodGet, Path: "/api/leader/queue", Handler: h(func(r *http.Request) (any, error) {
			return T.LeaderQueue(r.Context(), viewer(r), r.URL.Query().Get("team"))
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/reassign", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			var req struct {
				To   string `json:"to"`
				Note string `json:"note"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return nil, T.Reassign(r.Context(), viewer(r), id, req.To, req.Note)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/cancel", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			var req struct{ Note string }
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return nil, T.Cancel(r.Context(), viewer(r), id, req.Note)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/retrigger", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return nil, T.Retrigger(r.Context(), viewer(r), id)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/tasks/:id/retry-delivery", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			if !viewer(r).IsLeaderOf(viewer(r).Team) {
				return nil, task.Forbidden("只有 Leader 可以重试投递")
			}
			return nil, T.RetryDelivery(r.Context(), viewer(r).UserID, id)
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/rm-overrides", Handler: h(func(r *http.Request) (any, error) {
			return T.Overrides(r.Context())
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/rm-overrides", Handler: h(func(r *http.Request) (any, error) {
			var req task.OverrideReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return nil, T.AddOverride(r.Context(), viewer(r), req)
		})},
		rest.Route{Method: http.MethodDelete, Path: "/api/rm-overrides/:id", Handler: h(func(r *http.Request) (any, error) {
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return nil, T.RevokeOverride(r.Context(), viewer(r), id)
		})},

		// ---------- task types: catalog ----------
		rest.Route{Method: http.MethodGet, Path: "/api/node-types", Handler: h(func(r *http.Request) (any, error) {
			return sc.Engine.NodeTypes(), nil
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/capabilities", Handler: h(func(r *http.Request) (any, error) {
			return T.Caps().List(), nil
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/datasources", Handler: h(func(r *http.Request) (any, error) {
			return T.Sources().Names(), nil
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types", Handler: h(func(r *http.Request) (any, error) {
			return T.ListTypes(r.Context())
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req task.CreateTypeReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.CreateType(r.Context(), viewer(r).UserID, req)
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key", Handler: h(func(r *http.Request) (any, error) {
			key := pathStr(r, "key")
			tt, err := T.Type(r.Context(), sc.Conn, key)
			if err != nil {
				return nil, err
			}
			sched, _ := T.ScheduleInfo(r.Context(), key)
			return map[string]any{"type": tt, "dedupe_fields": tt.DedupeFields(), "schedule": sched}, nil
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/status", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req task.StatusReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.ChangeStatus(r.Context(), viewer(r).UserID, pathStr(r, "key"), req)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/run-now", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			return T.RunNow(r.Context(), viewer(r).UserID, pathStr(r, "key"))
		})},

		// ---------- TaskConfig line ----------
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/config-draft", Handler: h(func(r *http.Request) (any, error) {
			return T.ConfigDraft(r.Context(), viewer(r).UserID, pathStr(r, "key"))
		})},
		rest.Route{Method: http.MethodPut, Path: "/api/task-types/:key/config-draft", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req struct {
				Spec task.ConfigSpec `json:"spec"`
				Note string          `json:"note"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.SaveConfigDraft(r.Context(), viewer(r).UserID, pathStr(r, "key"), req.Spec, req.Note)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/dry-run", Handler: h(func(r *http.Request) (any, error) {
			key := pathStr(r, "key")
			tt, err := T.Type(r.Context(), sc.Conn, key)
			if err != nil {
				return nil, err
			}
			var req struct {
				Spec *task.ConfigSpec `json:"spec"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			spec := req.Spec
			if spec == nil {
				d, err := T.ConfigDraft(r.Context(), viewer(r).UserID, key)
				if err != nil {
					return nil, err
				}
				spec = d.Spec
			}
			info, _ := T.ScheduleInfo(r.Context(), key)
			var wm int64
			if info != nil {
				wm = info["schedule"].(task.ScheduleRow).Watermark
			}
			return map[string]any{"dry_run": T.DryRunConfig(r.Context(), tt, spec, wm), "errors": T.ConfigCheck(tt, spec),
				"produced": spec.Produced(), "next_fires": task.NextFires(spec.Schedule.Cron, spec.Schedule.Timezone, sc.Clock.Now(), 3)}, nil
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/configs", Handler: h(func(r *http.Request) (any, error) {
			return T.ConfigVersions(r.Context(), pathStr(r, "key"))
		})},

		// ---------- Workflow line ----------
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/flow-draft", Handler: h(func(r *http.Request) (any, error) {
			d, err := T.FlowDraft(r.Context(), viewer(r).UserID, pathStr(r, "key"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"definition": d, "validation": T.FlowCheck(d)}, nil
		})},
		rest.Route{Method: http.MethodPut, Path: "/api/task-types/:key/flow-draft", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req task.FlowDraftReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			d, err := T.SaveFlowDraft(r.Context(), viewer(r).UserID, pathStr(r, "key"), req)
			if err != nil {
				return nil, err
			}
			return map[string]any{"definition": d, "validation": T.FlowCheck(d)}, nil
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/flow-validate", Handler: h(func(r *http.Request) (any, error) {
			var req task.FlowDraftReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			d := &engine.Definition{Graph: req.Graph, Cards: req.Cards}
			if len(d.Cards) == 0 {
				d.Cards = json.RawMessage("{}")
			}
			return T.FlowCheck(d), nil
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/flows", Handler: h(func(r *http.Request) (any, error) {
			return T.FlowVersions(r.Context(), pathStr(r, "key"))
		})},

		// ---------- publish ----------
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/publish-plan", Handler: h(func(r *http.Request) (any, error) {
			return T.PlanPublish(r.Context(), viewer(r).UserID, pathStr(r, "key"), r.URL.Query().Get("scope"))
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/publish", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req task.PublishReq
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.Publish(r.Context(), viewer(r).UserID, pathStr(r, "key"), req)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/rollback", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				return nil, err
			}
			var req struct {
				Kind    string `json:"kind"`
				Version int64  `json:"version"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.Rollback(r.Context(), viewer(r).UserID, pathStr(r, "key"), req.Kind, req.Version)
		})},

		// ---------- records & metrics ----------
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/tasks", Handler: h(func(r *http.Request) (any, error) {
			lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			return T.TypeTasks(r.Context(), viewer(r), pathStr(r, "key"), r.URL.Query().Get("status"), lim)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/tasks", Handler: h(func(r *http.Request) (any, error) {
			if v := viewer(r); v.Role != "admin" {
				return nil, task.Forbidden("只有管理员可以手动补建任务")
			}
			var req struct {
				Fields map[string]any `json:"fields"`
				Note   string         `json:"note"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			id, c, err := T.ManualCreate(r.Context(), viewer(r).UserID, pathStr(r, "key"), req.Fields, req.Note)
			return map[string]any{"task_id": id, "candidate": c}, err
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/metrics", Handler: h(func(r *http.Request) (any, error) {
			return T.TypeMetrics(r.Context(), pathStr(r, "key"))
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/audit", Handler: h(func(r *http.Request) (any, error) {
			if err := configurer(r); err != nil {
				if viewer(r).Role != "leader" {
					return nil, err
				}
			}
			return T.AuditLog(r.Context(), r.URL.Query().Get("target"), 200)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/sql/check", Handler: h(func(r *http.Request) (any, error) {
			var req struct{ Query string }
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			q, args, err := datasrc.Compile(req.Query, expr.Vars{})
			if err != nil {
				return map[string]any{"ok": false, "error": err.Error()}, nil
			}
			return map[string]any{"ok": true, "compiled": q, "params": len(args), "vars": datasrc.Vars(req.Query)}, nil
		})},
	)

	// ---------- service endpoints: business push & object events ----------
	routes(TokenAuth(sc.Config.PushToken),
		rest.Route{Method: http.MethodPost, Path: "/api/push/:key", Handler: h(func(r *http.Request) (any, error) {
			var req struct {
				EventID string         `json:"event_id"`
				Payload map[string]any `json:"payload"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			return T.Push(r.Context(), pathStr(r, "key"), req.EventID, req.Payload)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/object-events", Handler: h(func(r *http.Request) (any, error) {
			var req struct {
				EventID   string `json:"event_id"`
				Event     string `json:"event"`
				ObjectKey string `json:"object_key"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			n, err := T.ObjectEvent(r.Context(), req.Event, req.ObjectKey, req.EventID)
			return map[string]any{"closed": n}, err
		})},
	)

	// ---------- XXL-Job heartbeats: three fixed jobs ----------
	routes(TokenAuth(sc.Config.JobToken),
		rest.Route{Method: http.MethodPost, Path: "/api/jobs/trigger-tick", Handler: h(func(r *http.Request) (any, error) {
			n, err := T.TriggerTick(r.Context())
			return map[string]any{"scans": n}, err
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/jobs/timer-tick", Handler: h(func(r *http.Request) (any, error) {
			n, err := sc.Engine.TimerTick(r.Context(), 45*time.Second)
			return map[string]any{"timers": n}, err
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/jobs/outbox-tick", Handler: h(func(r *http.Request) (any, error) {
			n, err := T.OutboxTick(r.Context(), 45*time.Second)
			return map[string]any{"sent": n}, err
		})},
	)

	// ---------- Lark card callback ----------
	server.AddRoute(rest.Route{Method: http.MethodPost, Path: "/api/lark/callback", Handler: larkCallback(sc)})

	registerAI(server, sc, auth)

	if sc.Config.Dev {
		registerDev(server, sc, auth)
	}
}

// larkCallback handles url_verification and card button clicks. The click is
// re-authorised server side: the open_id is mapped to a member and the same
// Act path (assignee check, card revision, idempotency) applies.
func larkCallback(sc *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type      string `json:"type"`
			Challenge string `json:"challenge"`
			Token     string `json:"token"`
			OpenID    string `json:"open_id"`
			Action    struct {
				Value struct {
					TaskID     int64  `json:"task_id"`
					WorkItemID int64  `json:"work_item_id"`
					Button     string `json:"button"`
					CardRev    int64  `json:"card_rev"`
				} `json:"value"`
			} `json:"action"`
			MessageID string `json:"open_message_id"`
		}
		if err := bind(r, &body); err != nil {
			writeErr(w, r, err)
			return
		}
		if sc.Config.Lark.VerificationToken != "" && body.Token != sc.Config.Lark.VerificationToken {
			writeErr(w, r, &task.Error{Code: 401, Msg: "invalid lark token"})
			return
		}
		if body.Type == "url_verification" {
			writeJSON(w, map[string]string{"challenge": body.Challenge})
			return
		}
		var m task.Member
		if err := sc.Conn.QueryRowCtx(r.Context(), &m, "SELECT * FROM bw_team_member WHERE lark_open_id=? AND active=1", body.OpenID); err != nil {
			writeErr(w, r, task.Forbidden("Lark 用户未绑定 Bywork"))
			return
		}
		v := task.Viewer{UserID: m.UserID, Role: m.Role, Team: m.Team}
		val := body.Action.Value
		key := "lark:" + body.MessageID + ":" + strconv.FormatInt(val.WorkItemID, 10) + ":" + val.Button
		_, err := sc.Tasks.Act(context.WithoutCancel(r.Context()), v, val.TaskID, task.ActionReq{WorkItemID: val.WorkItemID, Button: val.Button, RequestKey: key, CardRev: val.CardRev})
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, map[string]any{"toast": map[string]string{"type": "success", "content": "已处理"}})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
