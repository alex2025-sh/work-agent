package svc

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/stores/sqlx"

	"bywork/internal/clock"
	"bywork/internal/config"
	"bywork/internal/datasrc"
	"bywork/internal/db"
	"bywork/internal/engine"
	"bywork/internal/seed"
	"bywork/internal/task"
)

type ServiceContext struct {
	Config    config.Config
	Conn      sqlx.SqlConn
	Clock     clock.Clock
	DevClock  *clock.Offset
	Engine    *engine.Engine
	Tasks     *task.Service
	DevSender *task.LogSender
}

func NewServiceContext(c config.Config) (*ServiceContext, error) {
	raw, conn, err := db.Open(c.DB.Driver, c.DB.DSN)
	if err != nil {
		return nil, err
	}
	var clk clock.Clock = clock.Real{}
	var dev *clock.Offset
	if c.Dev {
		dev = &clock.Offset{}
		clk = dev
	}
	host, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%d", host, os.Getpid())

	budget, err := time.ParseDuration(c.ScanBudget)
	if err != nil {
		budget = 45 * time.Second
	}
	sources := datasrc.NewSources(10*time.Second, 5000)
	sources.Add("demo", raw)
	for name, dsn := range c.ReadOnlySources {
		ro, err := sql.Open(c.DB.Driver, dsn)
		if err != nil {
			return nil, fmt.Errorf("datasource %s: %w", name, err)
		}
		sources.Add(name, ro)
	}
	caps := datasrc.NewRegistry()
	datasrc.RegisterDemo(caps)

	eng := engine.New(conn, clk, owner)
	var sender task.Sender
	var devSender *task.LogSender
	if c.Lark.Enabled {
		sender = &task.LarkSender{BaseURL: c.Lark.BaseURL, AppID: c.Lark.AppID, AppSecret: c.Lark.AppSecret}
	} else {
		devSender = task.NewLogSender()
		sender = devSender
	}
	tasks := task.New(conn, eng, caps, sources, clk, sender, task.Options{
		Owner: owner, LinkDomains: c.LinkDomains, SignKey: c.SignKey, WebBaseURL: c.WebBaseURL,
		DisplayTimezone: c.DisplayTimezone, ScanBudget: budget, OutboxMaxTry: c.OutboxMaxTry,
	})
	ctx := context.Background()
	if c.Seed && seed.Empty(ctx, conn) {
		logx.Info("seeding demo data")
		if err := seed.Run(ctx, conn, tasks, clk.Now()); err != nil {
			return nil, fmt.Errorf("seed: %w", err)
		}
		if c.MockCases > 0 {
			st, err := seed.Mock(ctx, conn, clk.Now(), c.MockCases, c.MockSeed)
			if err != nil {
				return nil, fmt.Errorf("mock: %w", err)
			}
			logx.Infof("mock data: %d cases %v", st.Cases, st.ByScenario)
		}
	}
	return &ServiceContext{Config: c, Conn: conn, Clock: clk, DevClock: dev, Engine: eng, Tasks: tasks, DevSender: devSender}, nil
}

// StartBackground runs the in-process outbox sender and, when enabled, the
// three heartbeats (in production XXL-Job calls the /jobs endpoints instead).
func (s *ServiceContext) StartBackground(ctx context.Context) {
	s.Tasks.StartOutboxWorker(ctx)
	if !s.Config.InternalTicker {
		return
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			s.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

type TickResult struct {
	Scans  int `json:"scans"`
	Timers int `json:"timers"`
	Outbox int `json:"outbox"`
}

func (s *ServiceContext) Tick(ctx context.Context) TickResult {
	var r TickResult
	var err error
	if r.Scans, err = s.Tasks.TriggerTick(ctx); err != nil {
		logx.Errorf("trigger tick: %v", err)
	}
	if r.Timers, err = s.Engine.TimerTick(ctx, 45*time.Second); err != nil {
		logx.Errorf("timer tick: %v", err)
	}
	if r.Outbox, err = s.Tasks.OutboxTick(ctx, 30*time.Second); err != nil {
		logx.Errorf("outbox tick: %v", err)
	}
	return r
}
