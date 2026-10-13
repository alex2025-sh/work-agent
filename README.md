# Bywork 任务平台（一期）

按《Bywork 任务平台整合设计 v2》实现的任务平台：任务类型定义"这是什么事、什么时候产生"，流程定义"怎么处理"。两条 VIP 需求（`VIP_CASE_CREATED`、`VIP_ONGOING_REVIEW`）作为第一批任务类型内置为演示数据。

- **后端**：Go + [go-zero](https://go-zero.dev)（`backend/`）
- **前端**：React + ReactFlow 11（`reactflow` 包，即 `@reactflow/core`、`minimap`、`controls`、`background`、`node-toolbar`、`node-resizer`）（`frontend/`）

## 快速开始

```bash
# 1. 前端构建（后端会直接托管 dist）
cd frontend && npm install && npm run build

# 2. 启动后端（默认 SQLite + 开发模式 + 自动灌入演示数据）
cd ../backend && go run . -f etc/bywork.yaml
# 打开 http://localhost:8888
```

前端开发时可 `cd frontend && npm run dev`（5173 端口，`/api` 代理到 8888）。

开发模式下右上角可以：

- **切换身份**：RM 甲 / RM 乙 / RM 丙（Lark 投递必然失败）/ Leader 林 / 配置人员 陈 / 管理员。
- **推进系统时间**：`+1h / +1d / +3d`，推进后会立即执行三个心跳，用来观察 T+3 复查、48h → 7 天刷新同一张卡片、节点定时作废等行为。
- **演示台**：模拟 CRM（新建 Case 触发业务 Push、进入 / 离开 Maker Review、关闭 Case 触发 `case.closed`），并查看实际发出的 Lark 卡片 JSON 渲染效果（同一任务原地更新同一条消息）。

## 测试

```bash
cd backend && go test ./...                                   # SQLite
MYSQL_USER=bywork MYSQL_PASSWORD=bywork scripts/e2e-local.sh  # MySQL：Go 端到端 + 浏览器端到端 + HTML 报告
```

本地端到端、MySQL 配置、mock 数据见 **[docs/e2e.md](docs/e2e.md)**。本地 MySQL 开发环境（自动生成 200 个 mock Case）：`go run . -f etc/bywork.mysql.yaml`。

`internal/task/task_test.go` 用可控时钟端到端覆盖了两条需求：48h Closed → 等待 → 满 7 天仍在 Maker Review 时刷新原卡片；首次发现已满 7 天直接进入 7d 节点；节点定时作废本轮；离开阶段以"Case 已失效"结束；延迟到 `case.created_at + 3d` 复查、第二轮只保留 Closed；点击时提醒时间已过则立即复查；Case 关闭事件自动关闭；幂等键（同键同参 / 同键异参）；旧卡片版本被拒绝；RM 映射缺失进入"需要关注"并由 Leader 指派；投递最终失败；成员离职；字段契约拦截与"一起发布"；单次上限熔断；影子运行（不推送、独立去重命名空间、启用时关闭影子任务）；Push 幂等与资格前置。另有 SQL 参数绑定 / 只读约束的单测。

## 目录

```
backend/
  bywork.go                 go-zero 入口（-print-schema mysql 输出 DDL）
  etc/bywork.yaml           配置
  internal/
    engine/                 流程引擎：图、节点注册、每步落库、token / timer / event、校验
    engine/nodes/           通用节点：开始、结束、等待、条件、取数
    task/                   任务域：TaskType / TaskConfig / 发布、扫描、Push、人工节点、卡片、投递 outbox、运营、查询
    datasrc/                只读 SQL（参数绑定 + 只读约束）与 data.* 能力
    expr/                   模板、时间表达式（case.created_at + 3d）、结构化条件
    handler/                HTTP 路由（RM / Leader / 配置 / Push / XXL-Job 心跳 / Lark 回调 / 开发接口）
    seed/                   演示成员、演示 CRM 数据、两条 VIP 需求的配置（走真实的草稿 → 发布 → 启用路径）
frontend/src/
  pages/MyTasks.tsx         RM · 我的待办与任务详情
  pages/LeaderQueue.tsx     Team Leader · 团队队列
  pages/type/ConfigTab.tsx  任务类型 · 采集配置
  pages/type/FlowTab.tsx    任务类型 · 处理流程（ReactFlow 编辑器）
  pages/type/CardTab.tsx    任务类型 · 卡片
  pages/type/PublishDialog.tsx 发布确认 · 一起发布
  pages/DevPanel.tsx        演示台
e2e/
  sop.e2e.mjs               浏览器端到端（Playwright），两条 SOP 全流程，逐步截图与断言
  report.mjs                生成 HTML 报告
scripts/e2e-local.sh        一键本地端到端（MySQL / SQLite）
docs/
  e2e.md                    本地端到端与 mock 数据
  implementation.md         设计 → 实现对照、生产部署说明、未完成项
  schema.mysql.sql          MySQL DDL
```

部署（MySQL、XXL-Job 三个心跳、Lark、只读数据源、SSO）见 [docs/implementation.md](docs/implementation.md)。
