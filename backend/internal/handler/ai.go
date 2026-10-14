package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/rest"

	"bywork/internal/ai"
	"bywork/internal/svc"
	"bywork/internal/task"
)

// registerAI exposes the configuration assistant. The model never reaches
// these endpoints: it only sees the tools ai.Service runs on the caller's
// behalf. Accepting a proposal is a separate request made by a person.
func registerAI(server *rest.Server, sc *svc.ServiceContext, auth func(http.HandlerFunc) http.HandlerFunc) {
	A := sc.AI
	enabled := func() error {
		if A == nil {
			return task.BadRequest("AI 配置助手未启用（配置 AI.Enabled）")
		}
		return nil
	}
	wrap := func(rs ...rest.Route) []rest.Route {
		for i := range rs {
			rs[i].Handler = auth(rs[i].Handler)
		}
		return rs
	}
	server.AddRoutes(wrap(
		rest.Route{Method: http.MethodGet, Path: "/api/ai/status", Handler: h(func(r *http.Request) (any, error) {
			if A == nil {
				return ai.Status{}, nil
			}
			return A.Status(), nil
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/task-types/:key/ai/sessions", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			return A.CreateSession(r.Context(), viewer(r), pathStr(r, "key"))
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/task-types/:key/ai/sessions", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			return A.Sessions(r.Context(), viewer(r), pathStr(r, "key"))
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/ai/sessions/:id", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			return A.SessionDetail(r.Context(), viewer(r), pathStr(r, "id"))
		})},
		rest.Route{Method: http.MethodGet, Path: "/api/ai/proposals/:id", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return A.Proposal(r.Context(), viewer(r), id, true)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/ai/proposals/:id/accept", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return A.Accept(r.Context(), viewer(r), id)
		})},
		rest.Route{Method: http.MethodPost, Path: "/api/ai/proposals/:id/reject", Handler: h(func(r *http.Request) (any, error) {
			if err := enabled(); err != nil {
				return nil, err
			}
			id, err := pathID(r, "id")
			if err != nil {
				return nil, err
			}
			return A.Reject(r.Context(), viewer(r), id)
		})},
	))

	// Streaming turn: Server-Sent Events. A turn may take minutes, so this
	// route gets its own timeout (it also raises the server's write timeout).
	server.AddRoutes(wrap(rest.Route{Method: http.MethodPost, Path: "/api/ai/sessions/:id/messages", Handler: func(w http.ResponseWriter, r *http.Request) {
		if err := enabled(); err != nil {
			writeErr(w, r, err)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := bind(r, &req); err != nil {
			writeErr(w, r, err)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, r, fmt.Errorf("streaming unsupported"))
			return
		}
		started := false
		var mu sync.Mutex
		emit := func(ev ai.Event) {
			mu.Lock()
			defer mu.Unlock()
			if !started {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(http.StatusOK)
				started = true
			}
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		err := A.Send(r.Context(), viewer(r), pathStr(r, "id"), req.Text, emit)
		if err != nil {
			if !started {
				writeErr(w, r, err)
				return
			}
			emit(ai.Event{Type: "error", Message: err.Error()})
		}
	}}), rest.WithTimeout(10*time.Minute))
}
