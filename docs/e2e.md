# 本地端到端测试（MySQL）

三组端到端测试：

| 组 | 位置 | 覆盖 |
| --- | --- | --- |
| Go 端到端 | `backend/internal/task/task_test.go`、`backend/internal/ai/ai_test.go` | 真实数据库 + 引擎 + 三个心跳，用可控时钟跑完两条 SOP 的全部分支；AI 助手的工具循环、提议 / 接受 / 过期、模拟 OpenClaw 协议、脱敏（25 个用例） |
| 浏览器 · SOP | `e2e/sop.e2e.mjs`（Playwright） | 启动真实服务，按业务顺序操作界面，每步截图，同时对页面和 API 断言（42 条断言） |
| 浏览器 · AI 配置助手 | `e2e/ai.e2e.mjs` + `e2e/fake-openclaw.mjs` | 后端以 `Provider: openclaw` 连接模拟 OpenClaw（真实的流式 + 客户端工具协议），见下文（63 条断言、18 张截图） |

都能跑在 MySQL 或 SQLite 上。

### AI 配置助手端到端覆盖什么

`fake-openclaw.mjs` 实现 OpenClaw 的 `/v1/chat/completions`（SSE 流式、分片的 `tool_calls`、`[DONE]`、错误流），内部是一个按对话内容走的小脚本，并把收到的每个请求记下来（`GET /__log`）供断言。浏览器测试在**全新数据库**上依次：

1. **入口与权限**：Leader 看不到「AI 助手」、接口返回 403；配置人员打开助手，显示模型服务。
2. **采集配置**：「把 48 小时改成 72 小时」→ 读草稿 → `propose_config` → 校验与试跑 → 查看 diff（只有一行变化）→ 接受，草稿 revision +1。同时核对发给 OpenClaw 的请求：Bearer token、`model`、7 个工具（无保存 / 发布类）、系统提示、`tool_call_id` 回传。
3. **处理流程**：「7 天提醒前再确认 VIP 身份」→ 模型第一次漏连「否」出口，Bywork 流程校验报错，模型据此修正后第二次通过；第一个提议标记为「已被新提议替代」。接受后流程编辑器出现新节点。
4. **卡片**：给卡片加「当前阶段」（`case.stage`）。
5. **发布后生效**：经标准发布检查一起发布；点「立即执行一次」后只为 Maker Review ≥ 72h 的 Case 建任务；任务详情显示新卡片字段；取消某用户 VIP 并推进 5 天后，该任务经 AI 新增的分支以「已不是 VIP」失效结束，仍是 VIP 的任务走「是」分支进入 7 天提醒。
6. **护栏**：字段契约不满足的提议不能接受（接口 400）；草稿被他人修改后旧提议 409 并标记失效、页面刷新为最新草稿；拒绝；样本查询中 uid、盈利为 `***`，OpenClaw 收到的全部请求里没有任何真实 UID；模型出错后会话可继续；刷新后恢复历史与提议状态，下一轮请求带上历史；审计记录 `ai_turn` / 提议生成 / 接受 / 拒绝。

## 前置条件

- Go 1.24+、Node 20+、`curl`
- MySQL 8（5.7 应该也可以，但没测过），以及 `mysql` 命令行客户端
- 一个能 `CREATE DATABASE` / `DROP DATABASE` 的账号。Go 测试会为每个用例新建一个 `bywork_t_*` 库，跑完删除；浏览器测试使用 `bywork_e2e` 库，**每次运行都会删掉重建**

例如：

```sql
CREATE USER 'bywork'@'%' IDENTIFIED BY 'bywork';
GRANT ALL ON *.* TO 'bywork'@'%';
```

## 一条命令跑完

```bash
MYSQL_USER=bywork MYSQL_PASSWORD=bywork scripts/e2e-local.sh
```

脚本依次执行：

1. 重建 `bywork_e2e` 库
2. 在 MySQL 上跑 Go 测试（`go test -v ./...`），输出保存到 `e2e/out/gotest.txt`
3. 构建前端和后端，安装 Playwright 与 Chromium（首次运行）
4. 启动后端（`backend/etc/bywork.e2e.yaml`，端口 18890，AI 为脚本模拟），跑两条 SOP 的浏览器端到端
5. 重建数据库，启动模拟 OpenClaw（端口 18891）和后端（`AI.Provider: openclaw`），跑 AI 配置助手的浏览器端到端
6. 生成报告 **`e2e/out/report.html`**（截图在 `e2e/out/shots/` 和 `e2e/out/ai/shots/`）

任何一层失败，脚本都以非零退出码结束，报告照样生成。

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `MYSQL_HOST` / `MYSQL_PORT` | `127.0.0.1` / `3306` | |
| `MYSQL_USER` / `MYSQL_PASSWORD` | `root` / 空 | |
| `E2E_DB` | `bywork_e2e` | 浏览器测试用的库，每次重建 |
| `E2E_PORT` | `18890` | 测试服务端口 |
| `E2E_DRIVER` | `mysql` | 设为 `sqlite` 则完全不需要 MySQL |
| `SKIP_GO` / `SKIP_BROWSER` / `SKIP_BUILD` | | 设为 `1` 跳过对应步骤（`SKIP_BUILD` 复用已有的 `frontend/dist`） |
| `SKIP_SOP` / `SKIP_AI` | | 设为 `1` 跳过 SOP 或 AI 配置助手的浏览器测试 |
| `FAKE_PORT` | `18891` | 模拟 OpenClaw 的端口 |
| `HEADED` | | 设为 `1` 打开浏览器窗口观看 |
| `PW_CHROMIUM` | | 使用本机已有的 Chromium，跳过 `npx playwright install` |

脚本是 bash，Windows 上请用 WSL 或 Git Bash，或者按下面的步骤分开执行。

## 分步执行

**只跑 Go 端到端（MySQL）**

```bash
cd backend
BYWORK_TEST_MYSQL_DSN='bywork:bywork@tcp(127.0.0.1:3306)/' go test -count=1 -v ./...
# 保留测试库用于排查：BYWORK_TEST_KEEP_DB=1
# 不设 BYWORK_TEST_MYSQL_DSN 则使用 SQLite
```

**只跑浏览器端到端**：服务必须是 Dev 模式，并且连的是**全新**数据库，因为脚本会把系统时钟往后推 6 天。

```bash
mysql -ubywork -pbywork -e "DROP DATABASE IF EXISTS bywork_e2e; CREATE DATABASE bywork_e2e CHARACTER SET utf8mb4"
cd frontend && npm install && npm run build && cd ..

cd backend
BYWORK_E2E_DRIVER=mysql BYWORK_E2E_PORT=18890 BYWORK_E2E_STATIC=$PWD/../frontend/dist \
BYWORK_E2E_DSN='bywork:bywork@tcp(127.0.0.1:3306)/bywork_e2e?charset=utf8mb4' \
BYWORK_E2E_AI_PROVIDER=scripted BYWORK_E2E_AI_URL=http://127.0.0.1:18891/v1 \
go run . -f etc/bywork.e2e.yaml &

cd ../e2e && npm install && npx playwright install chromium
BASE_URL=http://127.0.0.1:18890 node sop.e2e.mjs     # HEADED=1 可观看
node report.mjs                                       # → e2e/out/report.html
```

**只跑 AI 配置助手的浏览器端到端**：同样需要全新数据库；后端改用 `openclaw` 并指向模拟服务。

```bash
cd e2e && PORT=18891 node fake-openclaw.mjs &
cd ../backend
BYWORK_E2E_DRIVER=mysql BYWORK_E2E_PORT=18890 BYWORK_E2E_STATIC=$PWD/../frontend/dist \
BYWORK_E2E_DSN='bywork:bywork@tcp(127.0.0.1:3306)/bywork_e2e?charset=utf8mb4' \
BYWORK_E2E_AI_PROVIDER=openclaw BYWORK_E2E_AI_URL=http://127.0.0.1:18891/v1 \
go run . -f etc/bywork.e2e.yaml &
cd ../e2e && BASE_URL=http://127.0.0.1:18890 FAKE_URL=http://127.0.0.1:18891 node ai.e2e.mjs
```

把 `BYWORK_E2E_AI_URL` 指向真实的内部 OpenClaw（并在 yaml 中改 `APIKey`）可以手动试用，但 `ai.e2e.mjs` 的断言依赖模拟服务的固定回答，不适合直接对真实模型运行。

## Mock 数据

### 基础演示数据（固定，测试依赖它）

服务第一次连到空库时（`Seed: true`）写入。时间都相对于启动时刻计算，所以任何时候启动，场景都成立。

| Appeal ID | RM | 状态 | 演示什么 |
| --- | --- | --- | --- |
| AP-20261013-0532 | RM 乙 | 创建 2 小时，first_review | SOP 1 · 新建 Case → 延迟 → 3 天后复查 |
| AP-20261010-0288 | RM 甲 | 创建 80 小时 | SOP 1 · 点延迟时已过 3 天，立即复查 |
| AP-20261012-0640 | RM 丙 | 创建 1 小时 | SOP 1 · RM 丙的 Lark 身份必然投递失败 → Leader「投递失败」 |
| AP-20261011-0391 | RM 甲 | Maker Review 50 小时 | SOP 2 · 48h 节点 → Closed → 满 7 天刷新原卡片 |
| AP-20261009-0204 | RM 乙 | Maker Review 72 小时 | SOP 2 · 48h 无人处理 → 节点定时作废本轮 |
| AP-20261006-0417 | RM 甲 | Maker Review 7 天 + 30 分钟 | SOP 2 · 首次发现已满 7 天，直接 7 天节点 |
| AP-20261013-0551 | 无映射 | Maker Review 49 小时 | RM 映射缺失 → Leader「需要关注」 |
| AP-20261012-0777 | 非 VIP | Maker Review 60 小时 | 不满足资格，不建任务 |

成员：RM 甲 `rm_a`、RM 乙 `rm_b`、RM 丙 `rm_c`、Leader 林 `leader_l`、配置人员 陈 `cfg_c`、管理员 `admin`。开发模式下在右上角切换。

### 随机 mock 数据（量大，用于手动探索）

按真实比例生成 `AP-MOCK-*` Case 和 VIP 档案，覆盖每条 SOP 的分支：约 20% 非 VIP、8% 无 RM 映射、10% 非目标类型；Case 分布在新建、创建超过 3 天、Maker Review 0 到 10 天、已离开阶段、已关闭等状态。另外会加两位 RM（RM 丁、RM 戊）。生成器只写 CRM 源数据，任务由扫描或 Push 按正常规则产生，和真实 CRM 接入时一致。`TestMockDataScenarios` 会校验每个 mock Case 是否按它的场景建任务或不建任务。

**方式一：首次启动时自动生成**，使用 `backend/etc/bywork.mysql.yaml`（`MockCases: 200`）：

```bash
mysql -ubywork -pbywork -e "CREATE DATABASE IF NOT EXISTS bywork CHARACTER SET utf8mb4"
cd frontend && npm install && npm run build && cd ../backend
export BYWORK_MYSQL_DSN='bywork:bywork@tcp(127.0.0.1:3306)/bywork?charset=utf8mb4'
go run . -f etc/bywork.mysql.yaml          # http://localhost:8888
```

200 个 Case 大约产生 110 个任务，5 位 RM 每人 14 到 21 条待办。

**方式二：给已有的库追加**，追加后会立即扫描建任务：

```bash
go run . -f etc/bywork.mysql.yaml -mock 100 -mock-seed 42   # 相同 seed 生成相同的数据
```

注意：单次扫描新建的任务超过任务类型的「单次建任务上限」（演示配置是 200）时会熔断，扫描计划暂停。要么分批追加，要么在「采集配置」里调高上限后点「恢复扫描」。这本身也是熔断功能的演示。

## 常见问题

- **`基础演示数据不在初始状态`**：浏览器测试需要全新的库。用一键脚本（会自动重建），或者手动 DROP/CREATE 后重启服务。
- **`Access denied` / 无法创建数据库**：账号需要 `CREATE`、`DROP` 权限（Go 测试为每个用例建库）。
- **端口被占用**：设置 `E2E_PORT`。
- **Playwright 下载 Chromium 失败**：用 `PW_CHROMIUM=/path/to/chrome` 指定本机浏览器。
- **测试失败后排查**：看 `e2e/out/server.log`、`e2e/out/gotest.txt`、`e2e/out/report.html`；Go 测试可以加 `BYWORK_TEST_KEEP_DB=1` 保留数据库。
