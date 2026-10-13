package seed

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
)

// extraMembers widen the team so the leader queue and member load look real.
var extraMembers = []member{
	{"rm_d", "RM 丁", "VIP_RM", "rm", "ou_rm_d"},
	{"rm_e", "RM 戊", "VIP_RM", "rm", "ou_rm_e"},
}

// MockStats summarises what Mock generated, by the scenario each case hits.
type MockStats struct {
	Cases      int            `json:"cases"`
	VIPs       int            `json:"vips"`
	ByScenario map[string]int `json:"by_scenario"`
}

// Mock inserts n random CRM cases (AP-MOCK-*) with VIP profiles into the demo
// tables, spread over every path the two SOPs care about. It only writes
// source data: tasks appear on the next scan (or Push for new cases), exactly
// as they would from a real CRM. seed makes runs reproducible.
func Mock(ctx context.Context, conn sqlx.SqlConn, now time.Time, n int, seed int64) (*MockStats, error) {
	r := rand.New(rand.NewSource(seed))
	for _, m := range extraMembers {
		var c int64
		_ = conn.QueryRowCtx(ctx, &c, "SELECT COUNT(1) FROM bw_team_member WHERE user_id=?", m.id)
		if c == 0 {
			if _, err := conn.ExecCtx(ctx, "INSERT INTO bw_team_member (user_id, name, team, role, lark_open_id, active) VALUES (?,?,?,?,?,1)", m.id, m.name, m.team, m.role, m.open); err != nil {
				return nil, err
			}
		}
	}
	rms := []string{"rm_a", "rm_b", "rm_c", "rm_d", "rm_e"}
	levels := []string{"VIP 1", "VIP 2", "VIP 3", "VIP 4", "VIP 5"}
	sources := []string{"交易量", "资产", "邀请", "机构"}
	targetTypes := []string{"withdrawal_appeal", "account_appeal", "trading_appeal"}
	otherTypes := []string{"kyc_review", "p2p_dispute"}
	st := &MockStats{ByScenario: map[string]int{}}
	ms := func(d time.Duration) int64 { return clock.Ms(now.Add(-d)) }
	h := time.Hour

	var start int64
	_ = conn.QueryRowCtx(ctx, &start, "SELECT COUNT(1) FROM demo_case WHERE appeal_id LIKE 'AP-MOCK-%'")
	for i := 0; i < n; i++ {
		seq := int(start) + i + 1
		appeal := fmt.Sprintf("AP-MOCK-%05d", seq)
		uid := fmt.Sprintf("9%09d", r.Intn(1e9))

		// VIP profile: 80% VIP; of those 10% have no RM mapping (→ 需要关注)
		isVip, rm := 1, rms[r.Intn(len(rms))]
		scenario := ""
		switch p := r.Float64(); {
		case p < 0.2:
			isVip, rm, scenario = 0, "", "非 VIP（不建任务）"
		case p < 0.28:
			rm, scenario = "", "RM 映射缺失"
		}
		profit := fmt.Sprintf("%d", (r.Intn(200)+1)*1000)
		if _, err := conn.ExecCtx(ctx, "INSERT INTO demo_vip (uid, is_vip, level, source, six_month_profit, rm_user) VALUES (?,?,?,?,?,?)",
			uid, isVip, levels[r.Intn(len(levels))], sources[r.Intn(len(sources))], profit, rm); err != nil {
			return nil, err
		}
		st.VIPs++

		typ := targetTypes[r.Intn(len(targetTypes))]
		if r.Float64() < 0.1 {
			typ = otherTypes[r.Intn(len(otherTypes))]
			if scenario == "" {
				scenario = "非目标 Case 类型（不建任务）"
			}
		}
		status, stage, entry := "open", "first_review", ""
		var entered int64
		var created int64
		switch p := r.Float64(); {
		case p < 0.35: // SOP 1: fresh cases, some already past created+3d
			ageH := r.Intn(120) + 1
			created = ms(time.Duration(ageH) * h)
			if scenario == "" {
				if ageH > 72 {
					scenario = "SOP1 · 创建已超过 3 天（延迟即复查）"
				} else {
					scenario = "SOP1 · 新建 Case"
				}
			}
		case p < 0.85: // SOP 2: in Maker Review for 0–10 days
			stage = "maker_review"
			stayH := r.Intn(240)
			entered = ms(time.Duration(stayH)*h + time.Duration(r.Intn(60))*time.Minute)
			created = entered - int64(r.Intn(72)+1)*3600_000
			entry = fmt.Sprintf("MR-%05d-1", seq)
			if scenario == "" {
				switch {
				case stayH < 48:
					scenario = "SOP2 · Maker Review < 48h（暂不建任务）"
				case stayH < 168:
					scenario = "SOP2 · 48h–7 天（48h 节点）"
				default:
					scenario = "SOP2 · 已满 7 天（直接 7 天节点）"
				}
			}
		case p < 0.93:
			stage, created = "checker_review", ms(time.Duration(r.Intn(200)+1)*h)
			if scenario == "" {
				scenario = "已离开 Maker Review（不建任务）"
			}
		default:
			status, created = "closed", ms(time.Duration(r.Intn(200)+1)*h)
			if scenario == "" {
				scenario = "已关闭 Case（不建任务）"
			}
		}
		if _, err := conn.ExecCtx(ctx, "INSERT INTO demo_case (appeal_id, uid, case_type, status, stage, stage_entered_at, maker_review_entry_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
			appeal, uid, typ, status, stage, entered, entry, created, created); err != nil {
			return nil, err
		}
		st.Cases++
		st.ByScenario[scenario]++
	}
	return st, nil
}
