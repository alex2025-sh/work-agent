# 设计 → 实现对照

本文对照《Bywork 任务平台整合设计 v2》说明各项决策落在哪里，以及上线前还需要补齐的部分。

## 对象模型与版本

| 设计 | 实现 |
| --- | --- |
| TaskType 不可变，只能启用 / 停用；去重定义（对象类型 + 键字段 + 轮次字段）挂在 TaskType | `bw_task_type`；`task.CreateType`；`TaskType.DedupeKey`。没有修改 TaskType 的接口 |
| TaskConfig vN、Workflow vN 各自版本化，草稿 → 发布，同一时间一个生效版本 | `bw_task_config` / `wf_definition`，`version=0` 为草稿；生效版本记在 `bw_task_type.active_*_version` |
| 独立发布 / 一起发布，原子切换；发布任一边时校验"产出 ⊇ 需要" | `task.PlanPublish`、`task.Publish`（单事务切换）。三个范围的可用性与原因由 `PlanPublish.Options` 给出 |
| 在途锁定 | `bw_task.config_version / flow_version` 在创建时写入，run 绑定流程版本；新版本只影响新任务 |
| 回滚 = 复制旧版本为新版本再发布 | `task.Rollback` |
| 扫描切换：从当前水位继续或按时长回扫 | `PublishReq.Rescan`，`applySchedule` |
| 发布前检查：试跑、影响预估、字段契约、数据权限 | `PlanPublish`：配置校验、`DryRunConfig`（只读执行、样本、去重唯一、预计新建数 / 通知 RM 数）、流程校验、字段契约 |
| 卡片属于流程，随流程发版 | `wf_definition.cards`；人工节点 `config.card` 引用模板 |
| 快照字段 vs 实时读取；等待之后的判断必须实时读取 | 快照 = 字段映射产出；`data.*` 能力不进入契约。`engine.Validate` 对"等待 / 人工节点之后、未经取数节点刷新就用快照字段做条件"给出警告 |
| Task 三个状态 + 关闭原因 + 关注标记，状态与待办同事务更新 | `bw_task.status / close_reason / attention`；人工节点和引擎监听器在同一事务里更新 |

## 流程引擎

- `engine.NodeExecutor`：`Meta()`（含 JSON Schema，前端据此自动生成属性面板）、`ValidateConfig`、`Exits`、`Fields`（读 / 写字段，用于契约）、`Execute`；需要等待事件的节点另实现 `Resumer`。
- **每步落库**：执行前写 `wf_node_exec(running)` 检查点；执行器的副作用（待办、outbox、任务状态）和执行后检查点、token 推进在同一事务提交。
- **Token**：`wf_token`，一期一个 run 一个 token，表结构支持多个。
- **统一唤醒**：人工动作、定时到期都是 `engine.Event`，`wf_event(run_id, event_key)` 唯一键做幂等；同键同参返回原结果，同键异参拒绝。
- **租约**：run 级租约 + CAS（`wf_run.lease_until`），timer / 扫描计划同样用租约 + CAS 防重，不依赖 XXL-Job 路由策略。
- **图与布局分开存**：`graph` 只有节点、配置、连线；`layout` 只有坐标（以及编辑器里的备注 `_notes`）。
- 一期节点：开始、取数（`data.*`，失败挂起告警或重试）、人工处理、等待、条件（多出口 + 默认出口）、结束（关闭原因）。

### 人工节点

- 按钮：`complete` 完成本轮走出口；`external` 由服务端校验当前处理人后签发受控 URL、只记录"已打开"，可配置同时完成本轮；`delay` 延迟处理。
- 延迟：再次提醒时间是时间表达式（需求一 `case.created_at + 3d`）；点击时已过则立即复查；唤醒前用 TaskConfig 的 `live_refresh` 实时读取并重新校验资格，不满足则以"Case 已失效"结束；次数上限可配，超过后按钮不再出现（需求一第二轮只剩 Closed）。
- 节点定时：到期仍未完成则作废本轮待办，走"超时"出口；进入节点时已过期则直接走超时出口，不发过期卡片。
- 每一轮投递前重新解析处理人（临时例外优先，其次权威映射；离职成员视为缺失）。
- 无人处理：不设自动升级，待办一直保留。

## 调度（XXL-Job 三个固定心跳）

| 心跳 | HTTP（`X-Bywork-Token: <JobToken>`） | 实现 |
| --- | --- | --- |
| `bywork_trigger_tick` | `POST /api/jobs/trigger-tick` | `task.TriggerTick`：按到期时间领取扫描计划，领取时即按 cron 推进下一次执行（错过的多次只补一次），带时间预算，跑不完写检查点、下一次心跳继续；单次上限熔断；无事件时的失效复查 |
| `bywork_timer_tick` | `POST /api/jobs/timer-tick` | `engine.TimerTick` |
| `bywork_outbox_tick` | `POST /api/jobs/outbox-tick` | `task.OutboxTick`：事务内写 outbox，提交后进程内立即发送；失败退避重试，超过上限标记"投递失败" |

XXL-Job 配置：调度过期策略"忽略"，阻塞处理策略"丢弃后续调度"，建议每分钟。生产环境把 `InternalTicker` 设为 `false`。

cron 校验：5 段、不支持秒、必须明确时区。`due_at` 在写入时按业务字段算成 UTC 毫秒。

## 两条 VIP 需求

配置见 `backend/internal/seed/seed.go`，启动时通过真实的草稿 → 发布 → 启用路径安装。

- **需求一 `VIP_CASE_CREATED`**：Push（`case.created`）+ 定时回扫补漏；资格 = VIP、Case Type 在目标范围、Case 未关闭；流程 = 取数（VIP 档案）→ 人工「RM 跟进」（Closed / 延迟处理，`case.created_at + 3d`，最多 1 次）→ 结束。
- **需求二 `VIP_ONGOING_REVIEW`（方案 A）**：去重 = Appeal ID + `maker_review_entry_id`；定时扫描；资格 = VIP、目标类型、在 Maker Review、停留 ≥ 48h；流程 = 解析 RM → 条件「已满 7 天？」→ 48h 节点（升级并打开 Lark / Closed，节点定时 `stage_entered_at + 7d`）→ 等到满 7 天 → 刷新 Case → 条件「仍在 Maker Review？」→ 7d 节点（同一张卡片原地刷新）或以"Case 已失效"结束。

## 运行规则补充（设计 §运行规则补充）

1. 业务对象失效自动关闭：`invalidation.events`（`POST /api/object-events`）+ `invalidation.recheck_every`。
2. 首次启用必须选择起点（只处理启用之后 / 回扫指定时长），查询里用 `{{scan_from}}`；超出单次上限熔断。
3. 影子任务的去重键使用 `shadow:` 命名空间；启用时影子任务以"影子运行结束"关闭、不推送。
4. 停用后不再新建和推送；在途定时到期只记录（"任务类型已停用：只记录，不推送"），重新启用按当前状态继续。
5. 每轮投递前重新解析 RM；成员离职时其活动待办进入 Leader「需要关注」。
6. Lark 身份来自 `bw_team_member.lark_open_id`；映射不到标记"投递失败"，站内待办照常；卡片按 message_id 原地更新；Lark 回调走同一套服务端鉴权与卡片版本校验。
7. outbox 超过最大重试次数后停止重试并打"投递失败"关注标记。
8. 数据驻留与保留期：**未实现**，见下方待办。

## 安全与边界

- SQL：`{{var}}` 只能出现在值的位置，编译成占位符；只允许单条 SELECT / WITH，拒绝写语句、`FOR UPDATE`、多语句；有超时与行数上限（`datasrc.Compile`，见单测）。生产环境 `ReadOnlySources` 必须配置只读账号或只读副本。
- 敏感字段：字段映射上的敏感级别；卡片按查看人角色脱敏（RM / Leader 看完整值，其他角色看掩码）。
- 所有动作服务端按当前身份鉴权：只有当前处理人（或团队 Leader）可以点按钮或外跳；客户端传入的处理人仅作筛选。
- 外跳：域名白名单 + HMAC 签名 URL，只记录"已打开"。
- 审计：配置创建 / 发布 / 回滚 / 状态变更、立即执行、熔断、外跳、改派、取消、重新触发、重试投递、补建、例外、成员变更写入 `bw_audit`。

## 生产部署

```yaml
DB:
  Driver: mysql
  DSN: user:pass@tcp(host:3306)/bywork?charset=utf8mb4
ReadOnlySources:
  dw_vip: ro_user:pass@tcp(replica:3306)/vip
Dev: false            # 关闭开发时钟、身份切换与 /api/dev/*
Seed: false
InternalTicker: false # 由 XXL-Job 调用三个心跳
JobToken: <secret>
PushToken: <secret>
SignKey: <secret>
Lark:
  Enabled: true
  AppID: ...
  AppSecret: ...
  VerificationToken: ...
```

- 身份：`X-Bywork-User` 必须由 SSO 网关注入，不能信任浏览器传入。
- 表结构：`docs/schema.mysql.sql`（`bywork -print-schema mysql` 生成；`demo_*` 两张表仅供演示）。启动时会自动建表。
- 演示用的 `data.case.get` / `data.vip.profile` / RM 映射读的是 `demo_case` / `demo_vip`，生产需要换成 CRM、VIP 服务的适配器（保持能力名和产出字段不变）。

## 未实现 / 下一步

- 数据驻留：快照只存键、展示时实时读取的模式；任务、事件、执行记录的保留期与归档。
- 角色与权限的完整模型、"提交发布审核"的审批流（当前由配置人员直接发布并审计）。
- MCP 触发来源的执行器（当前可配置，但需通过业务 Push 接入）；AI 摘要与追问（卡片保留开关、Prompt Key 与版本，前端在摘要不可用时只显示事实）；编辑器内的 AI diff 建议。
- 在途任务迁移、并行与汇合、子流程、外部回调（模型已预留多 token 与 callback 事件）。
- 指标告警接入（调度延迟、连续失败次数、距上次成功时长目前可在接口 `/api/task-types/:key` 的 `schedule` 中查看，熔断写日志 `ALERT`）。
- 容量压测。
