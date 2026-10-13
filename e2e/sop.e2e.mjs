// Browser end-to-end run for the two SOP alerts (VIP_CASE_CREATED,
// VIP_ONGOING_REVIEW). Every step drives the real UI, takes a screenshot and
// asserts against both the page and the API.
//
// Needs a Bywork server in Dev mode on a FRESH database (the run moves the
// dev clock forward six days). See docs/e2e.md.
//
//   BASE_URL=http://localhost:18890 node sop.e2e.mjs
//
// Env: BASE_URL (default http://localhost:18890), E2E_OUT (default ./out),
//      HEADED=1 to watch the browser, PW_CHROMIUM=/path/to/chromium to use a
//      preinstalled browser instead of `npx playwright install chromium`.
import { chromium } from 'playwright';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const B = (process.env.BASE_URL || 'http://localhost:18890').replace(/\/$/, '');
const OUT = path.resolve(process.env.E2E_OUT || path.join(here, 'out'));
fs.mkdirSync(path.join(OUT, 'shots'), { recursive: true });
for (const f of fs.readdirSync(path.join(OUT, 'shots'))) if (f.endsWith('.png')) fs.unlinkSync(path.join(OUT, 'shots', f));
const steps = [];
let cur = null;

(async () => {
  const b = await chromium.launch({
    headless: process.env.HEADED !== '1',
    executablePath: process.env.PW_CHROMIUM || undefined,
    args: ['--no-sandbox'],
  });
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
  // lark.example.com is a placeholder domain: answer it locally so the popup's URL is observable
  await ctx.route('https://lark.example.com/**', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<h3>Lark（模拟）</h3>' }));
  const p = await ctx.newPage();
  const pageErrors = [];
  p.on('pageerror', (e) => pageErrors.push(e.message));
  p.on('console', (m) => m.type() === 'error' && pageErrors.push(m.text().slice(0, 200)));

  const as = async (u) => p.evaluate((u) => localStorage.setItem('bywork.user', u), u);
  const api = (path, user, method = 'GET', body) =>
    p.evaluate(async ([path, user, method, body]) => {
      const r = await fetch(path, { method, headers: { 'X-Bywork-User': user, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
      return (await r.json()).data;
    }, [path, user, method, body]);
  const step = (sop, title, desc) => {
    cur = { sop, title, desc, checks: [], shots: [] };
    steps.push(cur);
    console.log(`\n[${sop}] ${title}`);
  };
  const check = (name, ok, detail = '') => {
    cur.checks.push({ name, ok: !!ok, detail: String(detail).slice(0, 200) });
    console.log(`  ${ok ? 'PASS' : 'FAIL'} ${name} ${detail ? '· ' + String(detail).slice(0, 120) : ''}`);
  };
  let n = 0;
  const shot = async (caption, opts = {}) => {
    await p.waitForTimeout(700);
    const file = `shots/${String(++n).padStart(2, '0')}.png`;
    if (opts.locator) await p.locator(opts.locator).first().screenshot({ path: `${OUT}/${file}` });
    else await p.screenshot({ path: `${OUT}/${file}`, ...opts });
    cur.shots.push({ file, caption });
  };
  const text = async (sel = 'body') => (await p.locator(sel).first().innerText().catch(() => '')) || '';
  const taskByObject = async (type, obj) => (await api(`/api/task-types/${type}/tasks?limit=500`, 'admin')).find((t) => t.object_key === obj);
  const detail = async (id) => api(`/api/tasks/${id}`, 'admin');
  const advance = async (d) => {
    await p.click(`header button:has-text("+${d}")`);
    await p.waitForTimeout(1500);
  };
  const openTask = async (obj) => {
    await p.click(`.task-item:has-text("${obj}")`);
    await p.waitForTimeout(600);
  };

  await p.goto(B + '/my');
  // preconditions: dev mode, seeded, untouched timeline
  const me = await api('/api/me', 'admin').catch(() => null);
  if (!me || !me.dev) throw new Error(`${B} 不可用，或未开启 Dev 模式（需要 Dev: true、Seed: true）`);
  // the e2e config runs no background ticker: run the first scan explicitly
  await api('/api/dev/tick', 'admin', 'POST', {});
  const pre = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261011-0391');
  if (!pre || pre.status !== 'PROCESSING' || pre.badge !== '48h')
    throw new Error('基础演示数据不在初始状态：端到端脚本需要全新数据库（重建库后重启服务）');
  const clock0 = (await api('/api/dev/clock', 'admin')).now;

  // ===================== SOP 2 · VIP_ONGOING_REVIEW =====================
  step('SOP 2', '扫描建任务：48h 与已满 7 天的 Case',
    '启用时回扫 30 天。AP-…0391 在 Maker Review 停留 50 小时 → 48h 节点；AP-…0417 首次发现已满 7 天 → 直接进入 7 天节点；非 VIP 的 AP-…0777 不建任务。');
  await as('rm_a'); await p.goto(B + '/my'); await p.waitForTimeout(800);
  const t391 = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261011-0391');
  const t417 = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261006-0417');
  const t777 = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261012-0777');
  check('AP-0391 进入 48h 节点', t391 && t391.badge === '48h' && t391.status === 'PROCESSING', t391 && `${t391.badge} / ${t391.status}`);
  check('AP-0417 直接进入 7 天节点', t417 && t417.badge === '7 天', t417 && t417.badge);
  const d417 = await detail(t417.id);
  check('AP-0417 没有发过期的 48h 卡片', !d417.work_items.some((w) => w.node_key === 'rm_48h'), d417.work_items.map((w) => w.node_key).join(','));
  check('非 VIP 的 AP-0777 不建任务', !t777);
  await openTask('AP-20261011-0391');
  await shot('RM 甲 · 我的待办：48h 与 7 天两张卡；详情回答「为什么 / 为什么是我 / 接下来」');
  const body1 = await text('.workbench');
  check('详情展示三问', body1.includes('为什么有这个任务') && body1.includes('VIP_UID_TO_RM') && body1.includes('节点定时到期'));

  step('SOP 2', 'Lark 卡片①：48 小时首次提醒', '开发投递器记录了实际发出的 Lark 交互卡片 JSON，这里按 Lark 样式渲染。');
  await p.click('button:has-text("查看 Lark 卡片")');
  await shot('任务详情内的 Lark 卡片预览（48h）', { clip: { x: 360, y: 60, width: 1080, height: 840 } });
  const lark0 = await api('/api/dev/lark', 'admin');
  const firstMsg391 = lark0.find((m) => JSON.stringify(m.card).includes('AP-20261011-0391'));
  check('48h 卡片已投递到 Lark（ou_rm_a）', firstMsg391 && firstMsg391.open_id === 'ou_rm_a', firstMsg391 && firstMsg391.message_id);

  step('SOP 2', 'RM 在 48h 卡片上点击 Closed', 'Closed 配置了二次确认；完成本轮后进入「等到进入 Maker Review 满 7 天」。');
  await p.click('.panel button:has-text("Closed")');
  await shot('二次确认弹窗（可选备注）');
  await p.click('.modal button:has-text("确认")');
  await p.waitForTimeout(1200);
  await shot('点击后：卡片显示结果态与「满 7 天会在这张卡上再次提醒」');
  const d391a = await detail(t391.id);
  check('任务转为 OPEN（等待中）', d391a.task.status === 'OPEN', d391a.task.status);
  check('下一次提醒 = 进入 Maker Review + 7 天', d391a.task.next_action_at > clock0 && Math.abs(d391a.task.next_action_at - (clock0 + 118 * 3600e3)) < 120e3, new Date(d391a.task.next_action_at).toISOString());
  check('等待态文案', (d391a.view.waiting || '').includes('如仍在 Maker Review'), d391a.view.waiting);
  await p.goto(B + '/my?g=waiting'); await p.waitForTimeout(800);
  await shot('「等待提醒」分组显示下一次提醒时间');

  // ===================== SOP 1 · VIP_CASE_CREATED =====================
  step('SOP 1', 'CRM 新建 Case → 业务 Push → 建任务', '演示台模拟 CRM 创建 VIP Case 并推送 case.created 事件（event_id 去重），资格满足后为当前 RM 建任务。');
  await as('admin'); await p.goto(B + '/dev'); await p.waitForTimeout(600);
  await p.fill('input[placeholder^="Appeal ID"]', 'AP-20261013-0999');
  await p.fill('input[placeholder="UID"]', '8812003390');
  await p.click('button:has-text("创建并推送")');
  await p.waitForTimeout(1200);
  await shot('演示台：新建 Case 并推送', { clip: { x: 0, y: 0, width: 1440, height: 420 } });
  const t999 = await taskByObject('VIP_CASE_CREATED', 'AP-20261013-0999');
  check('Push 建任务并分配给 RM 甲', t999 && t999.assignee === 'rm_a' && t999.status === 'PROCESSING', t999 && `${t999.assignee} / ${t999.status}`);
  const again = await api('/api/dev/cases', 'admin');
  check('Case 已写入 CRM 演示表', again.some((c) => c.appeal_id === 'AP-20261013-0999'));

  step('SOP 1', 'RM 点击「延迟处理」（No Response）', '再次提醒时间 = case.created_at + 3d，任务进入等待。');
  await as('rm_a'); await p.goto(B + '/my'); await p.waitForTimeout(800);
  await openTask('AP-20261013-0999');
  await shot('新建 Case 卡片：Closed / 延迟处理');
  await p.click('.panel button:has-text("延迟处理")');
  await p.waitForTimeout(1200);
  const d999a = await detail(t999.id);
  check('任务转为 OPEN，提醒时间 = 创建 + 3 天', d999a.task.status === 'OPEN' && Math.abs(d999a.task.next_action_at - (clock0 + 72 * 3600e3)) < 120e3, new Date(d999a.task.next_action_at).toISOString());
  check('「接下来」说明复查时间', d999a.explain.next.join(' ').includes('会复查 Case 并再次提醒'), d999a.explain.next.join(' '));
  await p.goto(B + `/my?g=waiting&t=${t999.id}`); await p.waitForTimeout(800);
  await shot('延迟后：等待提醒，「接下来会发生什么」给出复查时间');

  step('SOP 1', '点击延迟时已过创建 + 3 天 → 立即复查', 'AP-…0288 创建于 80 小时前；点延迟后不等待，立即复查并开始第 2 轮，第 2 轮只保留 Closed。');
  await p.goto(B + '/my'); await p.waitForTimeout(800);
  await openTask('AP-20261010-0288');
  await p.click('.panel button:has-text("延迟处理")');
  await p.waitForTimeout(1200);
  const t288 = await taskByObject('VIP_CASE_CREATED', 'AP-20261010-0288');
  const d288 = await detail(t288.id);
  const btn288 = d288.view.card.buttons.map((x) => x.label);
  check('立即进入第 2 轮', d288.view.card.round === 2 && d288.task.status === 'PROCESSING', `round ${d288.view.card.round}`);
  check('第 2 轮只剩 Closed', btn288.length === 1 && btn288[0] === 'Closed', btn288.join(','));
  check('标题与标签切换为「复查提醒 / T+3」', d288.view.card.title === 'VIP Case 复查提醒' && d288.view.card.round_label === 'T+3');
  await p.goto(B + `/my?t=${t288.id}`); await p.waitForTimeout(800);
  await shot('第 2 轮：T+3 复查提醒，只剩 Closed；上一轮显示「延迟处理」');

  step('SOP 1', '延迟期间 Case 被关闭 → 自动失效', 'RM 乙对 AP-…0532 点延迟；随后 CRM 关闭该 Case 并推送 case.closed，任务以「Case 已失效」关闭，卡片同步更新。');
  await as('rm_b'); await p.goto(B + '/my'); await p.waitForTimeout(800);
  await openTask('AP-20261013-0532');
  await p.click('.panel button:has-text("延迟处理")');
  await p.waitForTimeout(800);
  await as('admin'); await p.goto(B + '/dev'); await p.waitForTimeout(600);
  await p.click('tr:has-text("AP-20261013-0532") button:has-text("关闭 Case")');
  await p.waitForTimeout(1200);
  const t532 = await taskByObject('VIP_CASE_CREATED', 'AP-20261013-0532');
  check('任务关闭，原因 = Case 已失效', t532.status === 'CLOSED' && t532.close_reason === 'invalidated', `${t532.status} / ${t532.close_reason}`);
  await as('rm_b'); await p.goto(B + `/my?g=done&t=${t532.id}`); await p.waitForTimeout(900);
  await shot('RM 乙 · 已结束：Case 已失效，时间线记录收到 case.closed');

  // ===================== advance 3 days =====================
  step('SOP 1', '推进 3 天 → 到点复查并在同一任务开始第 2 轮', '系统时间 +3d（同时执行扫描、timer、outbox 三个心跳）。AP-…0999 到达创建 + 3 天：实时读取 Case 与 VIP 档案、重新校验资格，仍需跟进 → 第 2 轮。');
  await as('rm_a'); await p.goto(B + '/my'); await p.waitForTimeout(600);
  await advance('3d');
  const d999b = await detail(t999.id);
  check('同一个任务进入第 2 轮', d999b.view.card.round === 2 && d999b.task.status === 'PROCESSING', `task #${t999.id} round ${d999b.view.card.round}`);
  const n999 = (await api('/api/task-types/VIP_CASE_CREATED/tasks?limit=500', 'admin')).filter((t) => t.object_key === 'AP-20261013-0999').length;
  check('没有生成新任务', n999 === 1, `${n999} 个任务`);
  check('时间线记录「到期复查：仍需跟进」', d999b.timeline.some((x) => x.text.includes('到期复查：仍需跟进')));
  check('SOP 2 的 48h 等待尚未到期', (await detail(t391.id)).task.status === 'OPEN');
  await p.goto(B + `/my?t=${t999.id}`); await p.waitForTimeout(900);
  await shot('3 天后：T+3 复查提醒（第 2 轮），时间线展示延迟与复查');
  await p.click('.panel button:has-text("Closed")');
  await p.waitForTimeout(1000);
  const d999c = await detail(t999.id);
  check('第 2 轮点 Closed → 处理完成', d999c.task.status === 'CLOSED' && d999c.task.close_reason === 'completed', d999c.task.close_reason);

  // ===================== advance 3 more days =====================
  step('SOP 2', '再推进 3 天 → 满 7 天仍在 Maker Review，刷新原卡片', 'AP-…0391 到达进入 Maker Review + 7 天：刷新 Case 状态，仍在 Maker Review → 进入 7 天节点。同一个任务、同一条 Lark 消息原地更新。');
  await advance('3d');
  const d391b = await detail(t391.id);
  check('同一个任务进入 7 天节点', d391b.view.card.round_label === '7 天' && d391b.task.status === 'PROCESSING', d391b.view.card.title);
  check('上一轮显示「48h Closed」', (d391b.view.previous || '').includes('48h Closed'), d391b.view.previous);
  const msgs391 = (await api('/api/dev/lark', 'admin')).filter((m) => JSON.stringify(m.card).includes('AP-20261011-0391'));
  check('Lark 消息 ID 不变（原地更新）', msgs391.length === 1 && msgs391[0].message_id === firstMsg391.message_id, msgs391.map((m) => m.message_id).join(','));
  const n391 = (await api('/api/task-types/VIP_ONGOING_REVIEW/tasks?limit=500', 'admin')).filter((t) => t.object_key === 'AP-20261011-0391').length;
  check('一轮 Maker Review 只有一个任务', n391 === 1, `${n391} 个任务`);
  await p.goto(B + `/my?t=${t391.id}`); await p.waitForTimeout(900);
  await shot('7 天：同一任务刷新为升级提醒，上一轮「48h Closed」');

  step('SOP 2', 'Lark 卡片三个阶段（实际投递记录）', '同一条 Lark 消息先后呈现：① 48h 提醒 → ② Closed 后的结果态 → ③ 7 天刷新。下图是开发投递器里该消息的当前状态与更新次数。');
  await as('admin'); await p.goto(B + '/dev'); await p.waitForTimeout(900);
  const lark1 = await api('/api/dev/lark', 'admin');
  const m391 = lark1.find((m) => JSON.stringify(m.card).includes('AP-20261011-0391'));
  check('AP-0391 的 Lark 消息被原地更新 ≥ 2 次', m391 && m391.updates >= 2, m391 && `${m391.message_id} 更新 ${m391.updates} 次`);
  await shot('演示台 · 已发送的 Lark 卡片（同一消息原地更新）', { locator: '.panel:has(h2:has-text("已发送的 Lark 卡片"))' });

  step('SOP 2', '48h 无人处理 → 节点定时作废本轮', 'RM 乙一直没处理 AP-…0204 的 48h 卡片；到 7 天时作废本轮待办，复查后进入 7 天节点。无人处理不会自动升级或改派。');
  const t204 = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261009-0204');
  const d204 = await detail(t204.id);
  const voided = d204.work_items.find((w) => w.node_key === 'rm_48h');
  check('48h 待办状态 = voided', voided && voided.status === 'voided', voided && voided.status);
  check('进入 7 天节点，上一轮显示「48h 未处理」', d204.view.card.round_label === '7 天' && (d204.view.previous || '').includes('未处理'), d204.view.previous);
  await as('rm_b'); await p.goto(B + `/my?t=${t204.id}`); await p.waitForTimeout(900);
  await shot('RM 乙：48h 未处理，7 天卡片；时间线记录「节点定时仍未处理，本轮待办作废」');

  step('SOP 2', '7 天卡片点击「升级并打开 Lark」', '服务端校验当前 RM 后签发受控 URL（域名白名单 + HMAC 签名），只记录「已打开」，并按配置完成本轮 → 任务处理完成。');
  await as('rm_a'); await p.goto(B + `/my?t=${t391.id}`); await p.waitForTimeout(900);
  await p.click('.panel button:has-text("升级并打开 Lark")');
  await p.waitForTimeout(400);
  const popupP = ctx.waitForEvent('page', { timeout: 5000 }).catch(() => null);
  await p.click('.modal button:has-text("确认")');
  const popup = await popupP;
  if (popup) await popup.waitForLoadState().catch(() => {});
  const url = popup ? popup.url() : '';
  if (popup) await popup.close();
  check('打开受控 URL（lark.example.com，带签名）', url.startsWith('https://lark.example.com/') && url.includes('bw_sig='), url.slice(0, 110));
  await p.waitForTimeout(1000);
  const d391c = await detail(t391.id);
  check('任务处理完成', d391c.task.status === 'CLOSED' && d391c.task.close_reason === 'completed');
  check('时间线记录「已打开」', d391c.timeline.some((x) => x.text.includes('已打开')));
  await p.goto(B + `/my?g=done&t=${t391.id}`); await p.waitForTimeout(900);
  await shot('已结束：完整时间线（48h → Closed → 等待 → 7 天 → 升级并打开 Lark）');

  // ===================== leader =====================
  step('运营', 'Leader 队列：RM 映射缺失与投递失败', 'AP-…0551 的 UID 没有 RM 映射 → 进入「需要关注」，不会被随机指派；RM 丙的 Lark 身份投递必然失败，重试达到上限后标记「投递失败」。');
  for (let i = 0; i < 4; i++) {
    await api('/api/dev/clock', 'admin', 'POST', { advance: '1h' });
  }
  await as('leader_l'); await p.goto(B + '/team'); await p.waitForTimeout(1000);
  const q = await api('/api/leader/queue', 'leader_l');
  const reasons = q.attention.flatMap((a) => a.reasons);
  check('需要关注：RM 映射缺失', reasons.includes('RM 映射缺失'));
  check('需要关注：投递失败（RM 丙）', reasons.includes('投递失败'), reasons.join(','));
  await shot('团队队列：需要关注 / 成员负载 / 临时 RM 例外 / 按放置时长');

  step('运营', 'Leader 指派 RM 映射缺失的任务', 'Leader 选择 RM 乙并指派；任务转为处理中，关注标记清除，卡片投递给 RM 乙。');
  await p.selectOption('tbody tr:has-text("AP-20261013-0551") select', 'rm_b');
  await p.click('tbody tr:has-text("AP-20261013-0551") button:has-text("指派")');
  await p.waitForTimeout(1200);
  const t551 = await taskByObject('VIP_ONGOING_REVIEW', 'AP-20261013-0551');
  check('任务指派给 RM 乙并转为处理中', t551.assignee === 'rm_b' && t551.status === 'PROCESSING' && t551.attention.length === 0, `${t551.assignee} / ${t551.status}`);
  await as('rm_b'); await p.goto(B + `/my?t=${t551.id}`); await p.waitForTimeout(900);
  await shot('RM 乙收到任务：「为什么是我」= 由 Leader 林改派');
  const d551 = await detail(t551.id);
  check('「为什么是我」说明来自 Leader 指派', d551.explain.why_me.join(' ').includes('改派'), d551.explain.why_me.join(' '));

  // ===================== config side =====================
  step('配置', '处理流程编辑器（ReactFlow）', '需求二的流程：解析 RM → 已满 7 天？→ 48h 节点（节点定时）→ 等待 → 刷新 → 仍在 Maker Review？→ 7 天节点 / Case 已失效。右侧是输入契约与校验结果。');
  await as('cfg_c'); await p.goto(B + '/types/VIP_ONGOING_REVIEW/flow'); await p.waitForTimeout(1500);
  const fv = await api('/api/task-types/VIP_ONGOING_REVIEW/flow-draft', 'cfg_c');
  check('流程校验通过', fv.validation.issues.every((i) => i.level !== 'error'));
  await shot('VIP_ONGOING_REVIEW 处理流程');

  step('配置', '发布前检查（一起发布）', '字段契约、试跑、流程校验、影响预估全部在发布前给出。');
  await p.click('button:has-text("发布…")');
  await p.waitForTimeout(1500);
  const plan = await api('/api/task-types/VIP_ONGOING_REVIEW/publish-plan?scope=both', 'cfg_c');
  check('发布前检查全部通过', plan.ok, plan.checks.filter((c) => c.level === 'error').map((c) => c.text).join(';'));
  await shot('发布确认弹窗');

  check('全程无前端报错', pageErrors.length === 0, pageErrors.join(' | '));
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify({ steps, pageErrors, at: new Date().toISOString() }, null, 1));
  const all = steps.flatMap((s) => s.checks);
  const failed = all.filter((c) => !c.ok).length;
  console.log(`\nTOTAL ${all.length - failed}/${all.length} passed, ${n} screenshots → ${OUT}`);
  await b.close();
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error('ABORT', e.message || e);
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify({ steps, pageErrors: [], abort: String(e.message || e), at: new Date().toISOString() }, null, 1));
  process.exit(2);
});
