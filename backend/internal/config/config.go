package config

import "github.com/zeromicro/go-zero/rest"

type Config struct {
	rest.RestConf

	DB struct {
		Driver string `json:",default=sqlite,options=sqlite|mysql"`
		DSN    string `json:",default=file:bywork.db"`
	}
	// ReadOnlySources maps a datasource name to a read-only DSN (数仓 / 只读副本).
	// "demo" defaults to the main database for the local demo.
	ReadOnlySources map[string]string `json:",optional"`

	Dev            bool   `json:",default=true"` // dev clock, X-Bywork-User login, demo endpoints
	Seed           bool   `json:",default=true"` // seed demo data when the DB is empty
	MockCases      int    `json:",default=0"`    // with Seed: also generate N random mock CRM cases
	MockSeed       int64  `json:",default=1"`    // random seed for reproducible mock data
	InternalTicker bool   `json:",default=true"` // run heartbeats in-process (no XXL-Job)
	JobToken       string `json:",default=dev-job-token"`
	PushToken      string `json:",default=dev-push-token"`
	StaticDir      string `json:",optional"` // serve the built frontend

	LinkDomains     []string `json:",default=[lark.example.com,crm.example.com]"`
	SignKey         string   `json:",default=dev-sign-key"`
	WebBaseURL      string   `json:",default=http://localhost:8888"`
	DisplayTimezone string   `json:",default=Asia/Kuala_Lumpur"`
	ScanBudget      string   `json:",default=45s"`
	OutboxMaxTry    int      `json:",default=6"`

	// AI configuration assistant (docs/ai-assistant.md). Provider "openclaw"
	// calls the OpenClaw Gateway's OpenAI-compatible endpoint; "openai" any
	// Chat Completions endpoint (e.g. LiteLLM); "scripted" a deterministic
	// stand-in for demos and tests.
	AI struct {
		Enabled       bool              `json:",default=false"`
		Provider      string            `json:",default=scripted,options=openclaw|openai|scripted"`
		BaseURL       string            `json:",default=http://127.0.0.1:18789/v1"`
		APIKey        string            `json:",optional,env=BYWORK_AI_API_KEY"`
		Model         string            `json:",default=openclaw/bywork"`
		Headers       map[string]string `json:",optional"`
		Stateful      bool              `json:",default=false"`
		SampleRows    string            `json:",default=masked,options=masked|none"`
		AllowedTables []string          `json:",default=[demo_case,demo_vip]"`
		MaxSteps      int               `json:",default=12"`
		TurnTimeout   string            `json:",default=5m"`
	}

	Lark struct {
		Enabled           bool   `json:",default=false"`
		BaseURL           string `json:",default=https://open.larksuite.com"`
		AppID             string `json:",optional"`
		AppSecret         string `json:",optional"`
		VerificationToken string `json:",optional"`
	}
}
