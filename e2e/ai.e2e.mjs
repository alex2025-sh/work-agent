// Browser end-to-end run for the AI configuration assistant.
//
// The Bywork server runs with `AI.Provider: openclaw` pointing at
// fake-openclaw.mjs, which speaks the OpenClaw Chat Completions protocol
// (streaming, client tools). Every step drives the real UI, screenshots it,
// and asserts on the page, the Bywork API and what was actually sent to the
// model service. The changes the assistant proposes are accepted, published
// and then shown to take effect: the scan uses the new threshold, the new
// workflow branch runs, the card shows the new field.
//
// Needs a FRESH database (the run publishes and moves the dev clock). See docs/e2e.md.
//
//   BASE_URL=http://127.0.0.1:18890 FAKE_URL=http://127.0.0.1:18891 node ai.e2e.mjs
import { chromium } from 'playwright';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const B = (process.env.BASE_URL || 'http://127.0.0.1:18890').replace(/\/$/, '');
const FAKE = (process.env.FAKE_URL || 'http://127.0.0.1:18891').replace(/\/$/, '');
const OUT = path.resolve(process.env.E2E_OUT || path.join(here, 'out'), 'ai');
fs.mkdirSync(path.join(OUT, 'shots'), { recursive: true });
for (const f of fs.readdirSync(path.join(OUT, 'shots'))) if (f.endsWith('.png')) fs.unlinkSync(path.join(OUT, 'shots', f));
const TYPE = 'VIP_ONGOING_REVIEW';
const steps = [];
let cur = null;

(async () => {
  const b = await chromium.launch({ headless: process.env.HEADED !== '1', executablePath: process.env.PW_CHROMIUM || undefined, args: ['--no-sandbox'] });
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
  const p = await ctx.newPage();
  const pageErrors = [];
  p.on('pageerror', (e) => pageErrors.push(e.message));
  p.on('console', (m) => {
    // the 409/400 responses below are expected; the browser logs failed fetches as console errors
    if (m.type() === 'error' && !/status of (409|400|403)/.test(m.text())) pageErrors.push(m.text().slice(0, 200));
  });

  const as = async (u) => p.evaluate((u) => localStorage.setItem('bywork.user', u), u);
  const call = (path, user, method = 'GET', body) =>
    p.evaluate(
      async ([path, user, method, body]) => {
        const r = await fetch(path, { method, headers: { 'X-Bywork-User': user, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
        const j = await r.json().catch(() => ({}));
        return { status: r.status, code: j.code, msg: j.msg, data: j.data };
      },
      [path, user, method, body],
    );
  const api = async (path, user = 'cfg_c', method = 'GET', body) => (await call(path, user, method, body)).data;
  const fakeLog = async () => (await fetch(FAKE + '/__log')).json();
  const step = (sop, title, desc) => {
    cur = { sop, title, desc, checks: [], shots: [] };
    steps.push(cur);
    console.log(`\n[${sop}] ${title}`);
  };
  const check = (name, ok, detail = '') => {
    cur.checks.push({ name, ok: !!ok, detail: String(detail).slice(0, 240) });
    console.log(`  ${ok ? 'PASS' : 'FAIL'} ${name} ${detail ? '· ' + String(detail).slice(0, 140) : ''}`);
  };
  let n = 0;
  const shot = async (caption, opts = {}) => {
    await p.waitForTimeout(500);
    const file = `shots/${String(++n).padStart(2, '0')}.png`;
    if (opts.locator) await p.locator(opts.locator).first().screenshot({ path: `${OUT}/${file}` });
    else await p.screenshot({ path: `${OUT}/${file}` });
    cur.shots.push({ file: `ai/${file}`, caption });
  };
  const openType = async (tab = 'config') => {
    await p.goto(`${B}/types/${TYPE}/${tab}`);
    await p.waitForSelector('h1');
    await p.waitForTimeout(600);
  };
  const openAssistant = async () => {
    if (!(await p.locator('.ai-drawer').count())) await p.click('button:has-text("AI 助手")');
    await p.waitForSelector('.ai-drawer .ai-input textarea');
  };
  // send a message and wait until the turn is over (input enabled again)
  const say = async (text) => {
    const before = await p.locator('.ai-msg.assistant, .ai-err').count();
    await p.fill('.ai-input textarea', text);
    await p.click('.ai-input button[type=submit]');
    await p.waitForFunction((n) => document.querySelectorAll('.ai-msg.assistant, .ai-err').length > n && !document.querySelector('.ai-input textarea').disabled, before, { timeout: 30000 });
    await p.waitForTimeout(400);
    return (await p.locator('.ai-msg.assistant, .ai-err').last().textContent()) || '';
  };
  const lastCard = () => p.locator('.ai-proposal').last();
  const tasks = async () => {
    const r = await api(`/api/task-types/${TYPE}/tasks?limit=200`, 'admin');
    return (Array.isArray(r) ? r : r?.items || []).filter((t) => !t.is_shadow);
  };
  const taskOf = async (appeal) => (await tasks()).find((t) => t.object_key === appeal);
  const flowDraft = async () => {
    const r = await api(`/api/task-types/${TYPE}/flow-draft`);
    return { ...r.definition, validation: r.validation };
  };
  const ageH = (c, now) => (now - c.stage_entered_at) / 3600e3;

  await p.goto(B + '/');
  const draft0 = await api(`/api/task-types/${TYPE}/config-draft`);
  const flow0 = await flowDraft();
  const type0 = (await api(`/api/task-types/${TYPE}`)).type;

  // ===================== entry & permissions =====================
  step('AI · 入口', '入口与权限：只有配置人员能使用', 'Team Leader 能查看任务类型，但看不到「AI 助手」，直接调用接口也被拒绝；配置人员打开助手，标题处显示当前接入的模型服务（OpenClaw · openclaw/bywork）。');
  await as('leader_l');
  await openType();
  check('Leader 页面上没有「AI 助手」按钮', (await p.locator('button:has-text("AI 助手")').count()) === 0);
  const denied = await call(`/api/task-types/${TYPE}/ai/sessions`, 'leader_l', 'POST');
  check('Leader 调用新建会话接口被拒绝（403）', denied.status === 403, denied.msg);
  await as('cfg_c');
  await openType();
  await openAssistant();
  const pill = await p.locator('.ai-head .pill').textContent();
  const pillTitle = await p.locator('.ai-head .pill').getAttribute('title');
  check('助手显示接入的模型服务', pill.includes('openclaw/bywork') && pillTitle.includes('openclaw'), pillTitle);
  check('首次打开显示说明与示例', (await p.locator('.ai-empty').count()) === 1);
  await shot('配置人员打开 AI 配置助手');

  // ===================== config change =====================
  step('AI · 采集配置', '对话修改采集配置：资格门槛 48h → 72h', '配置人员说「把 48 小时改成 72 小时」。模型先调用 get_context 读取草稿，再用 propose_config 提交完整的新配置；Bywork 执行配置校验、只读试跑和字段契约检查后保存提议。此时草稿没有任何变化。');
  await fetch(FAKE + '/__reset', { method: 'POST' });
  const r1 = await say('资格条件里的 48 小时太敏感了，把 48 小时改成 72 小时');
  check('助手流式回复了修改说明', r1.includes('72h') && r1.includes('校验通过'), r1);
  const chips1 = await p.locator('.ai-tool').allTextContents();
  check('界面显示工具调用：读取草稿 → 提交采集配置提议', chips1.some((t) => t.includes('读取当前草稿')) && chips1.some((t) => t.includes('提交采集配置提议')), chips1.join(' | '));
  check('提议卡片：采集配置 · 待确认 · 可接受', (await lastCard().textContent()).includes('采集配置') && (await lastCard().locator('button:has-text("接受到草稿")').count()) === 1);
  const info1 = await lastCard().locator('.ai-v.info').allTextContents();
  check('提议附带只读试跑结果', info1.some((t) => t.includes('试跑')), info1.join(' | '));
  const d1 = await api(`/api/task-types/${TYPE}/config-draft`);
  check('提议生成后草稿未变（模型不能写草稿）', d1.revision === draft0.revision && JSON.stringify(d1.spec).includes('"48h"'), `r${d1.revision}`);
  let L = await fakeLog();
  const first = L[0]?.body || {};
  check('发给 OpenClaw：Bearer token、model=openclaw/bywork、stream', L[0]?.auth === 'Bearer e2e-gateway-token' && first.model === 'openclaw/bywork' && first.stream === true, `${L[0]?.auth} ${first.model}`);
  const toolNames = (first.tools || []).map((t) => t.function.name);
  check('提供 7 个工具，没有保存/发布类工具', toolNames.length === 7 && !toolNames.some((t) => /save|publish|accept/.test(t)), toolNames.join(','));
  check('第一条是系统提示（带 prompt 规则）', first.messages?.[0]?.role === 'system' && first.messages[0].content.includes('不能保存、接受或发布'));
  const follow = L[1]?.body?.messages || [];
  const toolMsg = follow.find((m) => m.role === 'tool');
  const asstMsg = follow.find((m) => m.role === 'assistant' && m.tool_calls);
  check('工具结果以 role=tool 回传，tool_call_id 对应', toolMsg && asstMsg && toolMsg.tool_call_id === asstMsg.tool_calls[0].id, toolMsg?.tool_call_id);
  check('一轮共调用模型 3 次（读草稿 → 提议 → 总结）', L.length === 3, L.length);
  const closeBox = await p.locator('.ai-head button[aria-label="关闭"]').boundingBox();
  check('有会话后标题栏不溢出（关闭按钮在屏幕内）', closeBox && closeBox.x + closeBox.width <= 1440, JSON.stringify(closeBox));
  await shot('对话、工具调用与提议卡片');

  step('AI · 采集配置', '查看 diff 并接受到草稿', 'diff 只显示资格条件那一行的变化。接受时 Bywork 检查草稿仍是提议所基于的 revision，然后以「AI 提议 #N」为修改说明写入草稿。');
  await lastCard().locator('button:has-text("查看 diff")').click();
  await p.waitForSelector('.modal .diff .add');
  const add1 = await p.locator('.modal .diff .add').allTextContents();
  const rm1 = await p.locator('.modal .diff .rm').allTextContents();
  check('diff：48h 删除、72h 新增，没有其他改动', add1.join().includes('72h') && rm1.join().includes('48h') && add1.length === 2 && rm1.length === 2, `+${add1.length} -${rm1.length}`);
  await shot('提议 diff（采集配置）');
  await p.click('.modal button:has-text("接受到草稿")');
  await p.waitForSelector('.ai-proposal.accepted');
  const d2 = await api(`/api/task-types/${TYPE}/config-draft`);
  check('草稿 revision +1，包含 72h', d2.revision === draft0.revision + 1 && JSON.stringify(d2.spec).includes('"72h"'), `r${draft0.revision} → r${d2.revision}`);
  await p.waitForTimeout(600);
  const head = await p.locator('main, .main').first().textContent();
  check('配置页标题显示 AI 提议的修改说明', head.includes('AI 提议 #'), '');
  await shot('接受后：配置草稿 r2，说明来自 AI 提议');

  // ===================== workflow change =====================
  step('AI · 处理流程', '对话修改处理流程：7 天提醒前再确认 VIP 身份', '模型读取草稿和节点类型后，用操作列表提交流程修改。第一次漏连了新判断节点的「否」出口，Bywork 的流程校验返回错误；模型据此补上连线重新提交，第二个提议通过校验，第一个自动标记为「已被新提议替代」。');
  await fetch(FAKE + '/__reset', { method: 'POST' });
  const r2 = await say('满 7 天发提醒之前，再确认一次用户是不是还是 VIP；已经不是 VIP 的话就不提醒了，直接失效结束');
  check('助手说明了修改内容和自我修正', r2.includes('再次确认 VIP 身份') && r2.includes('已按校验结果补上'), r2);
  await p.waitForSelector('.ai-proposal.superseded');
  const cards2 = p.locator('.ai-proposal');
  const nCards = await cards2.count();
  const bad = cards2.nth(nCards - 2);
  const good = cards2.nth(nCards - 1);
  const badText = await bad.textContent();
  check('第一次提议：校验未通过（出口 "no" 没有去向）', badText.includes('没有去向') && badText.includes('已被新提议替代'), badText.slice(0, 160));
  check('第一次提议不能接受', (await bad.locator('button:has-text("接受到草稿")').count()) === 0);
  check('第二次提议：通过校验，可接受', (await good.locator('button:has-text("接受到草稿")').count()) === 1);
  const chips2 = await p.locator('.ai-tool').allTextContents();
  check('调用了「查看节点类型」', chips2.some((t) => t.includes('查看节点类型')));
  L = await fakeLog();
  const retryReq = L.find((r) => (r.body.messages || []).filter((m) => m.role === 'tool' && m.name === 'propose_flow').length === 1);
  check('校验错误作为工具结果回传给模型', retryReq && JSON.stringify(retryReq.body.messages).includes('没有去向'));
  await shot('流程提议：第一次校验失败，第二次通过');

  step('AI · 处理流程', '查看流程变更并接受', '流程提议的 diff 先列出新增/修改的节点和连线，再给出 graph 与卡片的 JSON 对比（不含布局坐标）。');
  await good.locator('button:has-text("查看 diff")').click();
  await p.waitForSelector('.modal .ai-change');
  const change = await p.locator('.modal .ai-change').textContent();
  check('变更：新增 3 个节点', ['vip_recheck', 'vip_check', 'end_not_vip'].every((k) => change.includes(k)), change);
  check('变更：still:yes 改连到 vip_recheck', change.includes('still:yes→vip_recheck') && change.includes('still:yes→rm_7d'), '');
  await shot('流程提议的变更列表与 diff');
  await p.click('.modal button:has-text("接受到草稿")');
  await p.waitForFunction(() => document.querySelectorAll('.ai-proposal.accepted').length >= 2);
  const f2 = await flowDraft();
  const keys2 = f2.graph.nodes.map((x) => x.key);
  check('流程草稿 revision +1，包含新节点', f2.revision === flow0.revision + 1 && ['vip_recheck', 'vip_check', 'end_not_vip'].every((k) => keys2.includes(k)), `r${flow0.revision} → r${f2.revision}`);
  check('接受后流程校验无错误', f2.validation.issues.every((i) => i.level !== 'error'), JSON.stringify(f2.validation.issues.filter((i) => i.level === 'error')));
  await p.click('.ai-head button[aria-label="关闭"]');
  await openType('flow');
  await p.waitForSelector('.react-flow__node');
  const nodeTexts = await p.locator('.react-flow__node').allTextContents();
  check('流程编辑器中出现新节点', ['再次确认 VIP 身份', '仍是 VIP？', '已不是 VIP'].every((t) => nodeTexts.some((x) => x.includes(t))), '');
  const vn = p.locator('.react-flow__node:has-text("再次确认 VIP 身份")');
  if (await vn.count()) await vn.first().scrollIntoViewIfNeeded();
  await shot('流程编辑器：AI 新增的 VIP 复核分支');

  // ===================== card change =====================
  step('AI · 卡片', '对话修改卡片：增加「当前阶段」', '卡片模板属于流程版本。模型用 set_card 提交整张卡片模板；字段 case.stage 由采集配置产出，字段契约检查通过。');
  await openType('card');
  await openAssistant();
  const r3 = await say('卡片上加上当前阶段（case.stage）');
  check('助手生成卡片提议', r3.includes('当前阶段') && r3.includes('校验通过'), r3);
  await lastCard().locator('button:has-text("接受到草稿")').click();
  await p.waitForFunction(() => document.querySelectorAll('.ai-proposal.accepted').length >= 3);
  const f3 = await flowDraft();
  check('卡片模板包含 case.stage，流程草稿 revision +1', JSON.stringify(f3.cards).includes('"case.stage"') && f3.revision === f2.revision + 1, `r${f3.revision}`);
  check('之前接受的流程改动保留', f3.graph.nodes.some((x) => x.key === 'vip_check'));
  await p.click('.ai-head button[aria-label="关闭"]');
  await p.waitForTimeout(600);
  await shot('卡片页（接受后的草稿）');

  // ===================== publish & effect =====================
  step('AI · 生效', '一起发布：AI 修改经过标准发布检查', '接受只写入草稿。发布仍走原来的「发布…」：字段契约、试跑、流程校验、影响预估都要通过，这里一起发布采集配置 r2 和处理流程 r3。');
  await p.click('button:has-text("发布…")');
  await p.waitForSelector('.modal');
  await p.waitForFunction(() => !document.querySelector('.modal button.primary')?.disabled, null, { timeout: 15000 });
  await shot('发布确认（含 AI 修改）');
  await p.click('.modal button.primary:has-text("发布")');
  await p.waitForSelector('.modal', { state: 'detached' });
  const type1 = (await api(`/api/task-types/${TYPE}`)).type;
  check('采集配置和处理流程都生成了新版本', type1.active_config_version === type0.active_config_version + 1 && type1.active_flow_version === type0.active_flow_version + 1, `config v${type1.active_config_version} · flow v${type1.active_flow_version}`);

  step('AI · 生效', '扫描使用新门槛：只为 Maker Review ≥ 72h 的 Case 建任务', '在采集配置页点「立即执行一次」。原配置（≥ 48h）会为 Maker Review 已 49h、50h 的 VIP Case 建任务；新配置下这些 Case 暂不建任务。');
  await openType('config');
  await p.click('button:has-text("立即执行一次")');
  await p.waitForSelector('.toast', { timeout: 15000 }).catch(() => {});
  await p.waitForTimeout(800);
  const now0 = (await api('/api/dev/clock', 'admin')).now;
  const cases = await api('/api/dev/cases', 'admin');
  const caseList = Array.isArray(cases) ? cases : cases?.items || [];
  const mr = caseList.filter((c) => c.stage === 'maker_review' && c.status === 'open');
  const ts1 = await tasks();
  const made = new Set(ts1.map((t) => t.object_key));
  const young = mr.filter((c) => ageH(c, now0) >= 48 && ageH(c, now0) < 72).map((c) => c.appeal_id);
  check('48h–72h 的 Case 没有建任务', young.length > 0 && young.every((a) => !made.has(a)), young.join(', '));
  check('≥ 72h 的 Case 建了任务', made.has('AP-20261009-0204') && made.has('AP-20261006-0417'), [...made].join(', '));
  await openType('records');
  await shot('任务记录：只有 ≥ 72h 的 Case');

  step('AI · 生效', '卡片显示 AI 新增的「当前阶段」字段', '新任务使用发布后的流程版本，任务详情和 Lark 卡片字段来自 AI 修改后的模板。');
  const t204 = await taskOf('AP-20261009-0204');
  const det = await api(`/api/tasks/${t204.id}`, 'admin');
  const fieldLabels = (det.fields || []).map((f) => f.label);
  check('任务详情字段包含「当前阶段」= maker_review', (det.fields || []).some((f) => f.label === '当前阶段' && String(f.value).includes('maker_review')), fieldLabels.join(','));
  await as('admin');
  await p.goto(`${B}/tasks/${t204.id}`);
  await p.waitForTimeout(1200);
  const detText = await p.locator('body').textContent();
  check('详情页显示「当前阶段」', detText.includes('当前阶段'));
  await shot('任务详情：卡片字段来自 AI 修改');

  step('AI · 生效', 'AI 新增的流程分支实际执行', 'AP-…0204 的用户被取消 VIP；推进 5 天后，48h 节点到期 → 刷新 Case → 仍在 Maker Review → 再次确认 VIP → 不是 VIP → 以「已不是 VIP」结束。仍是 VIP 的 AP-…0391（推进 1 天后越过 72h 门槛被扫描建任务）走「是」分支，进入 7 天提醒。');
  await api('/api/dev/cases/AP-20261009-0204/update', 'admin', 'POST', { vip: false });
  await api('/api/dev/clock', 'admin', 'POST', { advance: '1d' });
  await api('/api/dev/clock', 'admin', 'POST', { advance: '4d' });
  await api('/api/dev/tick', 'admin', 'POST');
  const a = await api(`/api/tasks/${t204.id}`, 'admin');
  const ex = (d, k) => (d.executions || []).filter((x) => x.node_key === k);
  check('AP-…0204：经过「再次确认 VIP 身份」', ex(a, 'vip_recheck').length > 0, (a.executions || []).map((x) => x.node_key).join('→'));
  check('AP-…0204：「仍是 VIP？」走 no', ex(a, 'vip_check').some((x) => x.exit === 'no'));
  check('AP-…0204：以 invalidated 结束于 end_not_vip', a.task.status.toLowerCase() === 'closed' && a.task.close_reason === 'invalidated' && ex(a, 'end_not_vip').length > 0, `${a.task.status}/${a.task.close_reason}`);
  const t391 = await taskOf('AP-20261011-0391');
  const c = t391 ? await api(`/api/tasks/${t391.id}`, 'admin') : null;
  check('AP-…0391：越过 72h 门槛后被扫描建任务', !!t391 && t391.created_at > now0);
  check('AP-…0391：「仍是 VIP？」走 yes → 7 天提醒', c && ex(c, 'vip_check').some((x) => x.exit === 'yes') && ex(c, 'rm_7d').length > 0, c ? (c.executions || []).map((x) => `${x.node_key}${x.exit ? ':' + x.exit : ''}`).join('→') : '');
  await p.goto(`${B}/tasks/${t204.id}`);
  await p.waitForTimeout(1200);
  await shot('AP-…0204：AI 新增分支执行后以失效结束');

  // ===================== guard rails =====================
  step('AI · 护栏', '不满足字段契约的提议不能接受', '要求卡片显示采集配置不产出的字段。Bywork 的字段契约检查拒绝该提议（没有「接受」按钮，接口也返回 400），模型向用户说明需要先补字段映射。');
  await as('cfg_c');
  await openType('card');
  await openAssistant();
  const r4 = await say('卡片里加上工单负责人 case.ticket_owner');
  const c4 = await lastCard().textContent();
  check('提议标记「校验未通过」，错误指出字段契约', c4.includes('校验未通过') && c4.includes('case.ticket_owner'), c4.slice(0, 160));
  check('没有接受按钮', (await lastCard().locator('button:has-text("接受到草稿")').count()) === 0);
  const sess = (await api(`/api/task-types/${TYPE}/ai/sessions`))[0];
  const detail4 = await api(`/api/ai/sessions/${sess.id}`);
  const p4 = detail4.proposals[detail4.proposals.length - 1];
  const acc4 = await call(`/api/ai/proposals/${p4.id}/accept`, 'cfg_c', 'POST');
  check('直接调用接受接口返回 400', acc4.status === 400, acc4.msg);
  check('助手解释了原因', r4.includes('字段映射'), r4);
  await shot('字段契约不满足：提议被拒绝');

  step('AI · 护栏', '草稿被他人修改后，旧提议失效', '助手生成「72h → 96h」提议后，另一位配置人员手工保存了采集配置草稿。再接受旧提议时 Bywork 发现 revision 已变，返回 409，提议标记为「草稿已变化，已失效」，草稿保持他人的修改。');
  await openType('config');
  await openAssistant();
  await say('把 72 小时改成 96 小时');
  const d5 = await api(`/api/task-types/${TYPE}/config-draft`);
  const other = structuredClone(d5.spec);
  other.max_per_run = 150;
  const saved = await call(`/api/task-types/${TYPE}/config-draft`, 'admin', 'PUT', { spec: other, note: '另一位配置人员手工修改' });
  check('他人保存草稿成功', saved.status === 200, saved.msg);
  await lastCard().locator('button:has-text("接受到草稿")').click();
  await p.waitForSelector('.toast');
  const toast = await p.locator('.toast').textContent();
  check('接受返回冲突提示', toast.includes('已被修改'), toast);
  await p.waitForSelector('.ai-proposal.stale');
  const d6 = await api(`/api/task-types/${TYPE}/config-draft`);
  check('草稿保留他人修改，没有 96h', d6.spec.max_per_run === 150 && !JSON.stringify(d6.spec).includes('96h'), `r${d6.revision}`);
  await p.waitForTimeout(600);
  const headNow = await p.locator('.main').first().textContent();
  check('页面刷新为他人保存后的草稿', headNow.includes(`配置草稿 r${d6.revision}`), `r${d6.revision}`);
  await shot('旧提议失效（revision 冲突）');

  step('AI · 护栏', '拒绝提议', '拒绝后提议状态为「已拒绝」，草稿不变。');
  const r7 = await say('把 72 小时改成 80 小时');
  check('生成新提议', r7.includes('80h'), r7);
  const rev7 = (await api(`/api/task-types/${TYPE}/config-draft`)).revision;
  await lastCard().locator('button:has-text("拒绝")').click();
  await p.waitForSelector('.ai-proposal.rejected');
  check('拒绝后草稿不变', (await api(`/api/task-types/${TYPE}/config-draft`)).revision === rev7);
  await shot('提议已拒绝');

  step('AI · 护栏', '发给模型的数据：只查白名单表，敏感列脱敏', '模型查看 demo_case 表结构并试跑一条 SQL。核对 OpenClaw 实际收到的全部请求：样本中的 uid、近 6 个月盈利显示为 ***，原始 UID 从未离开 Bywork。');
  await fetch(FAKE + '/__reset', { method: 'POST' });
  const r8 = await say('看看 demo_case 的样本数据');
  const chips8 = await p.locator('.ai-tool').allTextContents();
  check('调用了「查看表结构」和「试运行 SQL」', chips8.some((t) => t.includes('查看表结构')) && chips8.some((t) => t.includes('试运行 SQL')));
  check('助手回复没有复述具体值', r8.includes('脱敏') && !/\d{10}/.test(r8), r8);
  L = await fakeLog();
  const sampleMsg = L.flatMap((r) => r.body.messages || []).find((m) => m.role === 'tool' && m.name === 'sample_query');
  const sample = sampleMsg ? JSON.parse(sampleMsg.content).bywork_tool_result : {};
  check('样本行：uid 与盈利为 ***，阶段等普通列保留', (sample.rows || []).length > 0 && sample.rows.every((r) => r.uid === '***' && r.six_month_profit === '***' && r.stage === 'maker_review'), JSON.stringify((sample.rows || [])[0]));
  const allSent = JSON.stringify(await fakeLog());
  const uids = [...new Set(caseList.map((c) => c.uid))];
  check('发给模型的所有请求中不含任何真实 UID', uids.length > 0 && uids.every((u) => !allSent.includes(u)), `${uids.length} 个 UID`);
  await shot('查看样本：工具调用与脱敏');

  step('AI · 护栏', '模型服务出错不影响会话', 'OpenClaw 返回错误时，界面显示错误；会话随即可以继续使用。');
  const r9 = await say('触发模型错误');
  check('界面显示模型错误', r9.includes('overloaded'), r9);
  const r10 = await say('你能做什么？');
  check('出错后会话可继续', r10.includes('可以帮你'), r10);
  await shot('模型出错后继续对话');

  step('AI · 护栏', '会话持久化与审计', '刷新页面重新打开助手，历史消息与提议状态从服务端恢复；继续对话时 Bywork 把历史一并发给 OpenClaw（默认无状态）。提议的生成、接受、拒绝都写入审计日志。');
  await openType('config');
  await openAssistant();
  await p.waitForSelector('.ai-proposal');
  const statuses = await p.locator('.ai-proposal .pill').allTextContents();
  const users = await p.locator('.ai-msg.user').count();
  check('刷新后恢复历史消息', users >= 8, `${users} 条用户消息`);
  check('刷新后恢复提议状态（已接受 / 已替代 / 已失效 / 已拒绝）', ['已接受到草稿', '已被新提议替代', '草稿已变化，已失效', '已拒绝'].every((s) => statuses.some((x) => x.includes(s))), statuses.join(','));
  await fetch(FAKE + '/__reset', { method: 'POST' });
  await say('谢谢');
  L = await fakeLog();
  const hist = (L[0]?.body.messages || []).filter((m) => m.role === 'user').length;
  check('新一轮请求带上了此前的用户消息（无状态模式）', hist >= 9, `${hist} 条 user 消息`);
  const audit = await api(`/api/audit?target=${TYPE}`, 'admin');
  const acts = new Set((audit || []).map((x) => x.action));
  check('审计包含 ai_turn / 提议生成 / 接受 / 拒绝', ['ai_turn', 'ai_proposal_created', 'ai_proposal_accepted', 'ai_proposal_rejected'].every((x) => acts.has(x)), [...acts].filter((x) => x.startsWith('ai_')).join(','));
  await shot('刷新后恢复的会话');

  check('全程无前端报错', pageErrors.length === 0, pageErrors.join(' | '));
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify({ steps, pageErrors, at: new Date().toISOString() }, null, 1));
  const all = steps.flatMap((s) => s.checks);
  const failed = all.filter((c) => !c.ok).length;
  console.log(`\nAI TOTAL ${all.length - failed}/${all.length} passed, ${n} screenshots → ${OUT}`);
  await b.close();
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error('ABORT', e.stack || e.message || e);
  fs.writeFileSync(`${OUT}/results.json`, JSON.stringify({ steps, pageErrors: [], abort: String(e.message || e), at: new Date().toISOString() }, null, 1));
  process.exit(2);
});
