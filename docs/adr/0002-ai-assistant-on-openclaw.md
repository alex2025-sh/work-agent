# ADR 0002：AI 配置助手接入公司内部 OpenClaw 服务

- 状态：已采纳
- 日期：2026-10-13
- 取代：[ADR 0001](0001-ai-config-assistant-llm-access.md)

## 决策

AI 配置助手通过公司内部的 OpenClaw 服务调用模型，使用它的 OpenAI 兼容 Chat Completions 接口（流式 + 客户端 function tools）。OpenClaw 的部署、鉴权和权限由内部服务负责，Bywork 只是一个调用方。

## 理由

- 公司已有内部 OpenClaw 服务，业务方直接调用即可，不需要自己维护模型网关。
- 工具全部在 Bywork 后端执行，不需要在 OpenClaw 里开发插件或回调 Bywork。数据访问、字段契约、试跑、权限和审计都留在 Bywork 内部。
- 客户端只依赖 OpenAI 兼容协议，以后切换到 LiteLLM 或其他网关只需改配置（`Provider: openai`）。

## 后果

- Bywork 侧仍然保留业务约束：模型只能提交提议，接受时检查 revision，样本数据脱敏，只允许查询白名单表，并记录审计。这些约束针对的是 Bywork 自己的数据和配置，与 OpenClaw 的安全设置无关。
- 默认无状态，每次请求都带上历史；需要时可以开启 `Stateful`，让 OpenClaw 保存会话。
- 本地开发和 e2e 使用脚本模拟（`Provider: scripted`），不依赖模型服务。
