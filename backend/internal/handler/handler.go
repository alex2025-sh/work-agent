package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"

	"bywork/internal/svc"
	"bywork/internal/task"
)

type ctxKey struct{}

// Envelope is the JSON response shape: {code, msg, data}.
type Envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

func viewer(r *http.Request) task.Viewer {
	v, _ := r.Context().Value(ctxKey{}).(task.Viewer)
	return v
}

// Auth resolves the caller from X-Bywork-User. In production this header must
// be injected by the SSO gateway (never trusted from the browser); in dev mode
// the frontend's user switcher sets it.
func Auth(sc *svc.ServiceContext) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			user := r.Header.Get("X-Bywork-User")
			if user == "" {
				user = r.URL.Query().Get("as")
			}
			if user == "" {
				writeErr(w, r, &task.Error{Code: 401, Msg: "未登录"})
				return
			}
			m, err := sc.Tasks.Member(r.Context(), sc.Conn, user)
			if err != nil || m.Active != 1 {
				writeErr(w, r, &task.Error{Code: 401, Msg: "未知或已停用的用户 " + user})
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, task.Viewer{UserID: m.UserID, Role: m.Role, Team: m.Team})
			next(w, r.WithContext(ctx))
		}
	}
}

// TokenAuth guards service-to-service endpoints (XXL-Job heartbeats, business push).
func TokenAuth(token string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-Bywork-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeErr(w, r, &task.Error{Code: 401, Msg: "invalid service token"})
				return
			}
			next(w, r)
		}
	}
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var te *task.Error
	if errors.As(err, &te) {
		httpx.WriteJsonCtx(r.Context(), w, te.Code, Envelope{Code: te.Code, Msg: te.Msg, Data: te.Data})
		return
	}
	logx.WithContext(r.Context()).Errorf("%s %s: %v", r.Method, r.URL.Path, err)
	httpx.WriteJsonCtx(r.Context(), w, http.StatusInternalServerError, Envelope{Code: 500, Msg: err.Error()})
}

// h adapts a typed function into an http.HandlerFunc.
func h(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := fn(r)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, Envelope{Code: 0, Msg: "ok", Data: out})
	}
}

func bind(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return task.BadRequest("请求体格式错误: %v", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(pathvar.Vars(r)[name], 10, 64)
	if err != nil {
		return 0, task.BadRequest("无效的 %s", name)
	}
	return id, nil
}

func pathStr(r *http.Request, name string) string { return pathvar.Vars(r)[name] }
