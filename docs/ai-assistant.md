# AI 配置助手（接入 OpenClaw）

配置人员在任务类型页点「AI 助手」，用自然语言修改**采集配置**和**处理流程（含卡片）**。助手会读取当前草稿，生成修改**提议**并校验。配置人员看过 diff、点「接受到草稿」后，修改才写入草稿；发布仍在「发布…」里单独操作。

模型由公司内部的 OpenClaw 服务提供，Bywork 直接调用它的 OpenAI 兼容接口。决策过程见 [ADR 0002](adr/0002-ai-assistant-on-openclaw.md)。

## 1. 架构

```
浏览器 ──SSE──► Bywork  ai.Service.Send（一轮对话）
                  │  ① 系统提示 + 历史 + 用户消息 + 7 个工具定义
                  ├────────► OpenClaw  POST {BaseURL}/chat/completions  (stream, model=openclaw/<agent>)
                  │◄──────── 文本增量 / tool_calls
                  │  ② tool_calls 在 Bywork 后端执行（以当前用户身份）
                  │     get_context / list_node_types / list_capabilities /
                  │     describe_table / sample_query / propose_config / propose_flow
                  │  ③ 工具结果作为 role=tool 消息回传 → 回到 ①，直到模型不再调用工具
                  ▼
              bw_ai_session / bw_ai_message / bw_ai_proposal
```

- **工具都在 Bywork 侧执行**：OpenClaw 只负责模型推理，返回 `tool_calls`。工具直接复用 Bywork 现有能力：草稿读取、`ConfigCheck`、`FlowCheck`、字段契约检查、只读试跑。
- **只有提议，没有写入工具**：模型拿不到保存草稿、接受提议或发布的工具。`propose_*` 把提议和校验结果存进 `bw_ai_proposal`，由人来接受。
- **接受时检查 revision**：提议记录了生成时所基于的草稿 revision。如果草稿之后被改过，接受会返回 409，提议标记为 `stale`，需要让助手基于最新草稿重新生成。
- **默认无状态**：每次调用都由 Bywork 带上完整历史（最近 20 条用户和助手文本），不依赖 OpenClaw 的会话。如果设置 `Stateful: true`，Bywork 会传 `user=bywork:<session_id>`，由 OpenClaw 保存会话。

## 2. 接入 OpenClaw

`backend/etc/bywork.yaml`：

```yaml
AI:
  Enabled: true
  Provider: openclaw                     # openclaw | openai | scripted
  BaseURL: http://openclaw.internal/v1   # 内部 OpenClaw 服务的 OpenAI 兼容地址
  APIKey: ""                             # 也可用环境变量 BYWORK_AI_API_KEY；会作为 Authorization: Bearer 发送
  Model: openclaw/bywork                 # openclaw/<agentId>
  Headers: {}                            # 需要额外请求头时填写
  Stateful: false
  SampleRows: masked                     # masked：样本行脱敏后给模型；none：只给列名和行数
  AllowedTables: [demo_case, demo_vip]   # describe_table / sample_query / 配置 SQL 允许引用的表
  MaxSteps: 12                           # 一轮最多调用模型的次数
  TurnTimeout: 5m
```

对 OpenClaw 服务的要求只有一条：支持 Chat Completions 的流式响应和客户端 function tools，即返回 `tool_calls`，并接受后续 `role: "tool"` 消息。`Provider: openai` 走同一个客户端，可以直接对接 LiteLLM 或其他兼容接口。

**本地没有模型时**：使用 `Provider: scripted`。这是确定性的脚本模拟，界面上标注「脚本模拟（非 AI）」，只识别两类示例：「把 48 小时改成 72 小时」和「卡片加 case.stage（当前阶段）」。它走的是与真实模型完全相同的工具循环，开发配置和 e2e 默认用它。

## 3. 工具

| 工具 | 作用 |
| --- | --- |
| `get_context` | 返回任务类型和去重定义、采集配置草稿（spec + revision）、流程草稿（graph + cards + revision）、流程输入契约、校验问题、数据源和允许的表 |
| `list_node_types` | 返回节点类型及配置 JSON Schema |
| `list_capabilities` | 返回 `data.*` 取数能力 |
| `describe_table` | 返回白名单表的列名和类型，并标出敏感列 |
| `sample_query` | 只读试跑一条 SELECT，最多 10 行，敏感列显示为 `***` |
| `propose_config` | 提交完整的 ConfigSpec。依次执行配置校验、白名单检查、只读试跑和字段契约检查（流程需要的字段必须由配置产出），然后保存提议 |
| `propose_flow` | 提交操作列表，可用操作：`add_node` / `update_node` / `remove_node` / `connect` / `disconnect` / `set_card` / `remove_card`。操作应用到草稿副本，然后执行流程校验和字段契约检查，再保存提议（含变更摘要和前后版本） |

流程用操作列表而不是整张图，是为了让 diff 小且好审阅，也防止模型误删它不打算动的节点。新节点会自动排版在 `after` 指定的节点下方。

同一会话中，同类型的新提议会把上一个待确认的提议标记为 `superseded`。没有通过校验的提议（`ok=0`）不能接受。

## 4. 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/ai/status` | 是否启用，以及 provider、模型和 prompt 版本 |
| POST / GET | `/api/task-types/:key/ai/sessions` | 新建会话 / 我的会话列表（仅 config、admin 角色） |
| GET | `/api/ai/sessions/:id` | 会话消息和提议（只能看自己的会话，admin 除外） |
| POST | `/api/ai/sessions/:id/messages` | `{text}`，SSE 流式返回。事件类型：`delta` / `tool` / `proposal` / `error` / `done`。同一会话同时只能运行一轮 |
| GET | `/api/ai/proposals/:id` | 提议详情，包含 payload（before / after / change） |
| POST | `/api/ai/proposals/:id/accept` | 接受到草稿；草稿已变化时返回 409 |
| POST | `/api/ai/proposals/:id/reject` | 拒绝 |

审计动作：`ai_turn`（记录 provider 和 prompt 版本 `bywork.config-assistant/v1`）、`ai_proposal_created`、`ai_proposal_accepted`、`ai_proposal_rejected`。草稿保存说明为「AI 提议 #N：…」，会显示在采集配置草稿的标题旁和版本页里。

## 5. 测试

- `go test ./internal/ai/`：覆盖脚本模拟的配置提议 → 接受 → 过期（stale）、卡片提议、权限检查；用一个**模拟 OpenClaw** 的 httptest 服务验证 `Authorization`、`model`、流式分片 `tool_calls` 合并、`tool_call_id` 回传、无状态历史和错误流；另外覆盖脱敏、表白名单、无效流程提议。也可以在 MySQL 上跑：`BYWORK_TEST_MYSQL_DSN='bywork:bywork@tcp(127.0.0.1:3306)/' go test ./internal/ai/`。
- `scripts/e2e-local.sh`：
  - SOP 浏览器测试中包含脚本模拟模式下的一步（发送消息、提议、查看 diff、接受）。
  - 单独的 AI 浏览器测试（`e2e/ai.e2e.mjs`，63 条断言）：后端以 `Provider: openclaw` 连接 `e2e/fake-openclaw.mjs`，覆盖用对话修改采集配置、处理流程（含校验失败后模型自我修正）和卡片，接受、发布后验证实际生效（新门槛、新分支执行、新卡片字段），以及各项护栏。详见 [e2e.md](e2e.md)。

**尚未验证**：没有连接真实的 OpenClaw 服务。接口按 OpenAI Chat Completions 的流式协议实现，并用模拟服务测试过。接入时请先用 `Provider: openclaw` 发一条消息，确认对方会返回 `tool_calls`。如果服务不支持客户端 tools，模型只会回复文本，不会产生提议。
