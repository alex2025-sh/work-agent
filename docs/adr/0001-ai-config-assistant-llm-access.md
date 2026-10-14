# ADR 0001：AI 配置助手的模型接入方式（OpenClaw vs LiteLLM）

- 状态：已被 [ADR 0002](0002-ai-assistant-on-openclaw.md) 取代（采用公司内部 OpenClaw 服务）
- 日期：2026-10-13
- 范围：Bywork「通过 AI 对话生成采集配置和处理流程」功能的模型接入层

## 1. 背景

配置人员在 Bywork 里用自然语言描述规则，AI 产出 TaskConfig / Workflow 的修改提议。设计稿的硬约束：

1. AI 只产出 **diff 草稿**，不能发布、不能扩大权限；接受后仍可手改，照常试跑和发布。
2. 所有调用按**当前用户身份**鉴权，提议、接受、拒绝都进入审计；Prompt 按 Key + 版本管理。
3. 只读数据源样本可能含 UID、盈利等敏感字段（部分来自 EU 集群），发送给模型前要脱敏，且要能确认数据去向。

AI 的每一步都要靠 Bywork 自己的能力：读草稿、查节点 schema、在只读数据源上试跑 SQL、用 `ConfigCheck` / `FlowCheck` / 字段契约 / 试跑校验提议。也就是说，**工具（tools）必须由 Bywork 后端实现并执行**。

公司内部可用：**OpenClaw**、**LiteLLM**。

## 2. 两者是什么

| | LiteLLM | OpenClaw |
| --- | --- | --- |
| 定位 | 模型网关 / 代理：统一入口转发到各家模型 | 自托管的 AI Agent 产品：Gateway + 渠道 + 技能 + 记忆 + Agent 运行时 |
| 对外接口 | OpenAI 兼容 `/v1/chat/completions`；Anthropic 原生格式 `/anthropic/v1/messages`（passthrough）与 `/v1/messages` | Gateway 的 WebSocket；可选开启 OpenAI 兼容 `/v1/chat/completions`（默认关闭），`model` 字段指向 **Agent**，不是具体模型 |
| 谁执行工具 | 调用方（Bywork）自己：网关只转发 `tool_use` / `tool_calls` | OpenClaw 自己的 Agent 运行时，通过它的 Skills / 插件执行 |
| 治理能力 | 虚拟 key、按 key/团队预算、模型白名单、限流、用量与成本日志 | 面向单个操作者：Gateway token / 密码；渠道接入（Slack、Teams、WhatsApp 等 50+） |
| 状态 | 无状态（每次请求自带上下文） | 有长期记忆（Markdown/YAML 文件），会话由 Gateway 维护 |

## 3. 方案

### 方案 A：LiteLLM 作为模型网关，Agent 循环在 Bywork 后端（推荐）

```
前端对话面板 ──SSE──▶ Bywork 后端（Go）
                        ├─ Agent 循环：消息历史、调用模型、执行工具、回填结果
                        ├─ 工具：get_draft / list_node_types / sample_query / propose_config / propose_flow
                        │        （全部是 Bywork 内部函数，按当前用户鉴权）
                        └─ LLM 接口 ──HTTP──▶ LiteLLM ──▶ 公司允许的模型
```

- 工具执行、权限、审计、"不能发布"全部在 Bywork 代码里，模型只能"提议"。
- 模型接入抽象成一个 Go 接口，两个适配器：
  - **Anthropic 原生格式**（经 LiteLLM `/anthropic/v1/messages`，Anthropic Go SDK 把 base URL 指向 LiteLLM）：保留严格工具 schema、思考、提示缓存等原生能力。使用 Claude 时首选。
  - **OpenAI 兼容格式**（经 LiteLLM `/v1/chat/completions`）：网关上任何模型都能用，换模型不改代码；代价是部分原生能力在格式转换中丢失。
- 预算、限流、成本归属复用 LiteLLM 的虚拟 key（给 Bywork 发一个服务 key，按环境区分）。

### 方案 B：OpenClaw 作为 Agent，Bywork 能力做成 OpenClaw 技能

```
前端 ──▶ Bywork 后端 ──/v1/chat/completions──▶ OpenClaw Gateway ──▶ Agent 运行时 ──▶ 模型
                                                        └─ Skill：回调 Bywork API（读草稿、校验、提议）
```

- Agent 循环和工具调度在 OpenClaw，Bywork 要把每个工具暴露成 OpenClaw Skill / 插件，Skill 再回调 Bywork API。
- 问题：
  1. **身份断层**：OpenClaw HTTP 接口用 Gateway 级 token，官方定位是"能安全持有 Gateway 操作员凭证的受信后端"，不是按最终用户鉴权。要把"当前配置人员"的身份一路传到 Skill 回调，需要额外设计，容易出现越权。
  2. **控制面外移**："只能提议、不能发布"、提议校验、审计都依赖 Skill 和 OpenClaw 配置，不再由 Bywork 代码保证。
  3. **长期记忆**：OpenClaw 会把对话沉淀为长期记忆。配置对话里有 SQL、表结构、样本数据，进入跨会话记忆既有数据外泄风险，也有提示注入被"存起来、以后再触发"的风险（Palo Alto Networks 对 OpenClaw 的分析）。
  4. **安全记录**：2026 年 ClawHub 出现数百个恶意 Skill（窃取交易所 API key、钱包、SSH 凭证），以及一键 RCE（CVE-2026-25253，已在 v2026.1.29 修复）。对交易所业务的配置系统，引入一个有技能市场和宿主机执行能力的 Agent，攻击面明显变大。
  5. **调试与评估**：Agent 行为分散在两个系统，评估（用两条 SOP 的端到端框架给 AI 产出打分）需要跨系统编排。

### 方案 C：B 的变体——OpenClaw 只做渠道

AI 配置助手按方案 A 实现，同时把 Bywork 的"对话 → 提议"能力暴露为一个受限 API（或 MCP 服务器），让 OpenClaw 作为**又一个入口**（例如配置人员在 Lark/Slack 里发起"帮我加一条规则"），结果仍是 Bywork 里的待审提议，接受和发布只能在 Bywork 界面完成。

## 4. 对比

| 维度 | A：LiteLLM + 自建循环 | B：OpenClaw 承载 Agent |
| --- | --- | --- |
| 满足"只提议不发布" | ✅ 代码保证 | ⚠️ 依赖 Skill 实现与配置 |
| 按最终用户鉴权与审计 | ✅ 沿用现有 Viewer 与 `bw_audit` | ⚠️ Gateway token 级，需额外透传 |
| 校验闭环（错误回填给模型自纠） | ✅ 同进程调用现有校验器 | ⚠️ 跨服务回调 |
| 敏感数据控制 | ✅ 发送前脱敏，无长期记忆；需确认 LiteLLM 日志是否存请求体 | ❌ 长期记忆、技能生态都会接触数据 |
| 换模型 | ✅ 网关层切换 | ✅ OpenClaw 配置切换 |
| 预算 / 限流 / 成本 | ✅ LiteLLM 虚拟 key | ⚠️ 需自行处理或 OpenClaw 后面再接网关 |
| 开发量 | 中：自写循环（约 200–300 行 Go）+ 工具 + 前端面板 | 中高：Skill 开发 + 身份透传 + 回调 API + 部署运维 |
| 额外运维 | 无（LiteLLM 已有） | Bywork 专用 OpenClaw 实例的部署、升级、安全加固 |
| 多渠道（Lark 等） | 需另做（见方案 C） | ✅ 自带 |

## 5. 决定

**采用方案 A。** OpenClaw 不作为配置助手的执行层；如果需要在 Lark 等渠道里发起配置对话，后续按方案 C 把 OpenClaw 作为入口接到 Bywork 的提议 API。

理由：这个功能的价值和风险都在"工具执行 + 校验 + 权限"上，这部分必须由 Bywork 掌控；需要的模型侧能力只是"稳定、可治理地调用模型"，正好是 LiteLLM 的职责。

## 6. 落地前需要平台团队确认

1. 内部 LiteLLM 的**版本**，以及是否开放 Anthropic 原生路由（`/anthropic/v1/messages` 或 `/v1/messages`）。部分版本存在原生格式请求**不计入 key 花费、预算不生效**的问题（BerriAI/litellm#24204、#23981，已有修复 PR），需要确认所用版本已包含修复。
2. 网关上**可用的模型**清单，以及工具调用 / 流式在这些模型上的支持情况。
3. LiteLLM 是否**记录请求和响应正文**（Spend Logs / 回调到日志平台）；如果记录，样本数据即使脱敏也需评估，必要时为 Bywork 的 key 关闭正文记录。
4. 网关部署区域与 EU 数据驻留要求是否冲突（与设计稿「数据驻留与保留期」待确认项一致）。
5. 为 Bywork 发放的虚拟 key 的预算、限流与按环境划分方式。

## 7. 后果

- Bywork 新增：`ai` 包（LLM 接口与两个适配器、Agent 循环、工具）、`bw_ai_session` / `bw_ai_proposal` 表、`/api/task-types/:key/ai/chat`（SSE）、前端对话面板与 diff 预览。
- 模型、Prompt 版本可配置；以两条 SOP 的自然语言描述为输入，用现有 Go 端到端框架评估 AI 产出的配置能否通过同样的断言。
- 不引入新的长期运行服务。

## 参考

- LiteLLM：[Anthropic Passthrough](https://docs.litellm.ai/docs/pass_through/anthropic_completion)、[/v1/messages](https://docs.litellm.ai/docs/anthropic_unified/)、[Proxy Quick Start（虚拟 key、预算）](https://docs.litellm.ai/docs/proxy/quick_start)、[#24204](https://github.com/BerriAI/litellm/issues/24204)、[#23981](https://github.com/BerriAI/litellm/issues/23981)、[PR #24205](https://github.com/BerriAI/litellm/pull/24205)
- OpenClaw：[OpenAI chat completions（Gateway HTTP API）](https://docs.openclaw.ai/gateway/openai-http-api)、[架构概览](https://www.fast.io/resources/openclaw-architecture-overview.md)、[7 OpenClaw Security Challenges](https://www.digitalocean.com/resources/articles/openclaw-security-challenges)、[341 个恶意 ClawHub Skill](https://thehackernews.com/2026/02/researchers-find-341-malicious-clawhub.html)、[ClawHub 接入 VirusTotal 扫描](https://thehackernews.com/2026/02/openclaw-integrates-virustotal-scanning.html)
