package handler

import (
	"net/http"

	"github.com/zeromicro/go-zero/rest"

	"bywork/internal/expr"
	"bywork/internal/svc"
	"bywork/internal/task"
)

// registerDev exposes demo-only endpoints: shift the clock to watch T+3 / 48h /
// 7d behaviour, run the heartbeats on demand, inspect sent Lark cards, and
// mutate demo CRM cases. Never enabled in production (Dev=false).
func registerDev(server *rest.Server, sc *svc.ServiceContext, auth func(http.HandlerFunc) http.HandlerFunc) {
	rs := []rest.Route{
		{Method: http.MethodGet, Path: "/api/dev/clock", Handler: h(func(r *http.Request) (any, error) {
			return map[string]any{"now": sc.Clock.Now().UnixMilli(), "offset_ms": sc.DevClock.Offset().Milliseconds()}, nil
		})},
		{Method: http.MethodPost, Path: "/api/dev/clock", Handler: h(func(r *http.Request) (any, error) {
			var req struct {
				Advance string `json:"advance"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			d, err := expr.ParseDuration(req.Advance)
			if err != nil {
				return nil, task.BadRequest("%v", err)
			}
			sc.DevClock.Advance(d)
			res := sc.Tick(r.Context())
			return map[string]any{"now": sc.Clock.Now().UnixMilli(), "tick": res}, nil
		})},
		{Method: http.MethodPost, Path: "/api/dev/tick", Handler: h(func(r *http.Request) (any, error) {
			return sc.Tick(r.Context()), nil
		})},
		{Method: http.MethodGet, Path: "/api/dev/lark", Handler: h(func(r *http.Request) (any, error) {
			if sc.DevSender == nil {
				return []any{}, nil
			}
			return sc.DevSender.List(), nil
		})},
		{Method: http.MethodGet, Path: "/api/dev/cases", Handler: h(func(r *http.Request) (any, error) {
			var rows []struct {
				AppealID           string `db:"appeal_id" json:"appeal_id"`
				UID                string `db:"uid" json:"uid"`
				CaseType           string `db:"case_type" json:"case_type"`
				Status             string `db:"status" json:"status"`
				Stage              string `db:"stage" json:"stage"`
				StageEnteredAt     int64  `db:"stage_entered_at" json:"stage_entered_at"`
				MakerReviewEntryID string `db:"maker_review_entry_id" json:"maker_review_entry_id"`
				CreatedAt          int64  `db:"created_at" json:"created_at"`
			}
			err := sc.Conn.QueryRowsPartialCtx(r.Context(), &rows, "SELECT appeal_id, uid, case_type, status, stage, stage_entered_at, maker_review_entry_id, created_at FROM demo_case ORDER BY id DESC")
			return rows, err
		})},
		{Method: http.MethodPost, Path: "/api/dev/cases", Handler: h(func(r *http.Request) (any, error) {
			// create a case and push case.created to VIP_CASE_CREATED (simulates CRM)
			var req struct {
				AppealID string `json:"appeal_id"`
				UID      string `json:"uid"`
				CaseType string `json:"case_type"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			now := sc.Clock.Now().UnixMilli()
			if _, err := sc.Conn.ExecCtx(r.Context(), "INSERT INTO demo_case (appeal_id, uid, case_type, status, stage, created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
				req.AppealID, req.UID, req.CaseType, "open", "first_review", now, now); err != nil {
				return nil, err
			}
			var vip struct {
				IsVip  int64  `db:"is_vip"`
				Level  string `db:"level"`
				Source string `db:"source"`
				Profit string `db:"six_month_profit"`
			}
			_ = sc.Conn.QueryRowPartialCtx(r.Context(), &vip, "SELECT is_vip, level, source, six_month_profit FROM demo_vip WHERE uid=?", req.UID)
			return sc.Tasks.Push(r.Context(), "VIP_CASE_CREATED", "case.created:"+req.AppealID, map[string]any{
				"appeal_id": req.AppealID, "uid": req.UID, "case_type": req.CaseType, "status": "open", "created_at": now,
				"is_vip": vip.IsVip, "vip_level": vip.Level, "vip_source": vip.Source, "six_month_profit": vip.Profit,
			})
		})},
		{Method: http.MethodPost, Path: "/api/dev/cases/:appeal/update", Handler: h(func(r *http.Request) (any, error) {
			// move a case: {"status":"closed"} or {"stage":"maker_review"}; closing emits case.closed
			var req struct {
				Status string `json:"status"`
				Stage  string `json:"stage"`
			}
			if err := bind(r, &req); err != nil {
				return nil, err
			}
			id, now := pathStr(r, "appeal"), sc.Clock.Now().UnixMilli()
			if req.Stage != "" {
				entry := id + "-" + expr.ToString(now)
				if _, err := sc.Conn.ExecCtx(r.Context(), "UPDATE demo_case SET stage=?, stage_entered_at=?, maker_review_entry_id=?, updated_at=? WHERE appeal_id=?", req.Stage, now, entry, now, id); err != nil {
					return nil, err
				}
			}
			closed := 0
			if req.Status != "" {
				if _, err := sc.Conn.ExecCtx(r.Context(), "UPDATE demo_case SET status=?, updated_at=? WHERE appeal_id=?", req.Status, now, id); err != nil {
					return nil, err
				}
				if req.Status == "closed" {
					n, err := sc.Tasks.ObjectEvent(r.Context(), "case.closed", id, "")
					if err != nil {
						return nil, err
					}
					closed = n
				}
			}
			return map[string]any{"closed_tasks": closed}, nil
		})},
	}
	for i := range rs {
		rs[i].Handler = auth(rs[i].Handler)
	}
	server.AddRoutes(rs)
}
