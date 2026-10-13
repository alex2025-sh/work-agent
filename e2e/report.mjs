// Builds out/report.html from out/results.json (browser run) and, if present,
// out/gotest.txt (`go test -v ./...` output). Usage: node report.mjs [outDir]
import fs from 'node:fs';
import path from 'node:path';
import { execSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(process.argv[2] || process.env.E2E_OUT || path.join(here, 'out'));
const css = "\n/* layout: single reading column; summary tiles, then per-SOP step cards with checks beside evidence screenshots */\n:root {\n  --bg: #f6f7f5; --surface: #ffffff; --ink: #18211d; --muted: #5d6a63; --line: #dfe4e0;\n  --accent: #17634f; --accent-soft: #e2efe9; --ok: #17634f; --bad: #b42318; --bad-soft: #fde8e6;\n  --code-bg: #eef2ef;\n  --f-body: \"Noto Sans SC\", \"PingFang SC\", \"Microsoft YaHei\", system-ui, sans-serif;\n  --f-mono: \"IBM Plex Mono\", ui-monospace, SFMono-Regular, Menlo, monospace;\n}\n@media (prefers-color-scheme: dark) { :root:not([data-theme=\"light\"]) {\n  --bg: #111613; --surface: #18201c; --ink: #e5ece8; --muted: #9aa9a1; --line: #2a3530;\n  --accent: #5cc3a1; --accent-soft: #1d3a30; --ok: #5cc3a1; --bad: #ff8a7a; --bad-soft: #3b1d1a; --code-bg: #212b26; color-scheme: dark } }\n:root[data-theme=\"dark\"] {\n  --bg: #111613; --surface: #18201c; --ink: #e5ece8; --muted: #9aa9a1; --line: #2a3530;\n  --accent: #5cc3a1; --accent-soft: #1d3a30; --ok: #5cc3a1; --bad: #ff8a7a; --bad-soft: #3b1d1a; --code-bg: #212b26; color-scheme: dark }\nbody { background: var(--bg); color: var(--ink); font: 15px/1.65 var(--f-body); }\n.wrap { max-width: 1120px; margin: 0 auto; padding-inline: 20px; padding-block: 40px 72px; display: grid; gap: 40px; }\nh1, h2, h3 { text-wrap: balance; margin: 0; line-height: 1.3; }\nh1 { font-size: clamp(26px, 4vw, 34px); font-weight: 700; letter-spacing: .01em; }\nh2 { font-size: 22px; font-weight: 700; }\nh3 { font-size: 16px; font-weight: 500; }\ncode { font-family: var(--f-mono); font-size: 12.5px; }\n.eyebrow { margin: 0 0 6px; font: 500 12px var(--f-mono); letter-spacing: .08em; text-transform: uppercase; color: var(--accent); }\n.meta { display: flex; flex-wrap: wrap; gap: 6px 18px; color: var(--muted); font-size: 13px; margin-top: 10px; }\n.meta code { color: var(--ink); }\n.lede { max-width: 68ch; color: var(--muted); margin: 14px 0 0; }\n.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 12px; }\n.tile { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 16px 18px; min-width: 0; }\n.tile .n { font: 500 30px/1.1 var(--f-mono); font-variant-numeric: tabular-nums; color: var(--ok); }\n.tile .k { font-weight: 500; margin-top: 6px; }\n.tile .h { color: var(--muted); font-size: 12.5px; }\n.panel { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 20px 22px; min-width: 0; }\n.two { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 12px; }\n.panel h2 { font-size: 16px; margin-bottom: 8px; }\n.panel ul { margin: 0; padding-left: 1.1em; } .panel li { margin: 3px 0; }\n.clock { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 0; border-top: 2px solid var(--accent); margin-top: 10px; }\n.clock div { padding: 10px 12px 0 0; font-size: 13px; color: var(--muted); }\n.clock b { display: block; font: 500 13px var(--f-mono); color: var(--ink); }\n.sop { display: grid; gap: 18px; }\n.sophead { border-bottom: 1px solid var(--line); padding-bottom: 12px; }\n.blurb { margin: 6px 0 0; color: var(--muted); }\n.tally { font: 500 12.5px var(--f-mono); color: var(--ok); margin-left: 6px; white-space: nowrap; }\n.step { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 18px 20px 20px; display: grid; gap: 12px; min-width: 0; }\n.step header { display: flex; align-items: baseline; gap: 10px; }\n.step h3 { flex: 1; min-width: 0; }\n.sid { font: 500 12px var(--f-mono); color: var(--muted); }\n.sd { margin: 0; color: var(--muted); max-width: 80ch; }\n.badge { font: 500 12px var(--f-mono); padding: 1px 8px; border-radius: 999px; white-space: nowrap; }\n.badge.pass { background: var(--accent-soft); color: var(--ok); }\n.badge.fail { background: var(--bad-soft); color: var(--bad); }\n.checks { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }\n.checks li { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; font-size: 14px; }\n.checks .mk { width: 14px; font-weight: 700; color: var(--ok); }\n.checks .bad .mk, .checks .bad .cn { color: var(--bad); }\n.checks .cd { background: var(--code-bg); padding: 0 6px; border-radius: 4px; color: var(--muted); overflow-wrap: anywhere; min-width: 0; }\n.figs { display: grid; gap: 14px; grid-template-columns: 1fr; }\n.figs.n2, .figs.n3 { grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); }\nfigure { margin: 0; min-width: 0; }\nfigure img { display: block; width: 100%; height: auto; border: 1px solid var(--line); border-radius: 8px; background: #fff; }\nfigure a:focus-visible img { outline: 3px solid var(--accent); outline-offset: 2px; }\nfigcaption { font-size: 13px; color: var(--muted); margin-top: 6px; }\n.tablewrap { overflow-x: auto; }\ntable { width: 100%; border-collapse: collapse; font-size: 14px; }\nth { text-align: left; font-weight: 500; color: var(--muted); font-size: 12.5px; border-bottom: 1px solid var(--line); padding: 6px 8px; }\ntd { border-bottom: 1px solid var(--line); padding: 8px; vertical-align: top; }\ntr:last-child td { border-bottom: 0; }\ntd.num { font-family: var(--f-mono); font-variant-numeric: tabular-nums; text-align: right; white-space: nowrap; }\n.gdesc { color: var(--muted); font-size: 13px; }\n.tag { font: 500 11.5px var(--f-mono); color: var(--accent); white-space: nowrap; }\npre { background: var(--code-bg); border-radius: 8px; padding: 12px 14px; overflow-x: auto; font: 12.5px/1.6 var(--f-mono); margin: 8px 0 0; }\nnav.toc { display: flex; flex-wrap: wrap; gap: 8px; }\nnav.toc a { color: var(--accent); text-decoration: none; border: 1px solid var(--line); background: var(--surface); padding: 4px 12px; border-radius: 999px; font-size: 13px; }\nnav.toc a:hover { border-color: var(--accent); }\nnav.toc a:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }\n";
const R = JSON.parse(fs.readFileSync(path.join(OUT, 'results.json'), 'utf8'));
const gotxt = fs.existsSync(path.join(OUT, 'gotest.txt')) ? fs.readFileSync(path.join(OUT, 'gotest.txt'), 'utf8') : '';
const e = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const git = (cmd) => { try { return execSync(cmd, { cwd: here, stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim(); } catch { return ''; } };

const gotests = [...gotxt.matchAll(/--- (PASS|FAIL): (\w+) \(([\d.]+)s\)/g)].map((m) => [m[1], m[2], m[3]]);
const pkgs = [...gotxt.matchAll(/^(ok|FAIL)\s+(\S+)\s+([\d.]+s)/gm)].map((m) => [m[1], m[2], m[3]]);
const desc = {
  TestCompileBindsValues: ['安全', 'SQL 变量编译成占位符并按值绑定'],
  TestCompileRejects: ['安全', '拒绝表名/列名位置的变量、字符串内变量、写语句、多语句、FOR UPDATE'],
  TestSeedPublishesAndScans: ['平台', '两条需求经草稿 → 发布 → 启用；扫描建任务，重复扫描不重复建'],
  TestOngoingReviewFirstSeenAfter7Days: ['SOP 2', '首次发现已满 7 天：直接进入 7 天节点，不发过期 48h 卡片'],
  TestOngoingReview48hClosedThen7d: ['SOP 2', '48h Closed → 等待 → 满 7 天仍在 Maker Review → 同一任务、同一 Lark 消息刷新；外跳完成'],
  TestOngoingReviewDeadlineVoidsRound: ['SOP 2', '48h 无人处理：节点定时作废本轮，进入 7 天节点'],
  TestOngoingReviewLeftStage: ['SOP 2', '7 天前离开 Maker Review：以「Case 已失效」结束'],
  TestCaseCreatedDelayRemind: ['SOP 1', '延迟到 created_at + 3d；到点复查，第 2 轮只剩 Closed、标题切换'],
  TestCaseCreatedDelayAlreadyPassed: ['SOP 1', '点延迟时已过 3 天：立即复查并进入第 2 轮'],
  TestCaseCreatedDelayedCaseClosed: ['SOP 1', '延迟期间 Case 关闭：到点复查以「Case 已失效」结束'],
  TestObjectClosedEvent: ['两条', 'case.closed 事件关闭任务并作废待办；重复事件只处理一次'],
  TestActionIdempotencyAndStaleCard: ['平台', '幂等键同键同参 / 同键异参；非处理人被拒；旧卡片版本 409'],
  TestLeaderOps: ['运营', 'RM 映射缺失进需要关注、Leader 指派、投递失败标记、成员离职、取消'],
  TestDetailExplains: ['处理端', '三问说明；其他 RM 无权查看；敏感字段对配置角色脱敏'],
  TestPublishContractAndBreaker: ['配置', '字段契约拦截单独发布、一起发布通过；单次上限熔断'],
  TestShadowRun: ['配置', '影子运行不推送、不进待办；启用时关闭影子任务，真任务不受去重阻挡'],
  TestPushIdempotent: ['SOP 1', '业务 Push 按 event_id 幂等；同 Case 去重；不满足资格不建任务'],
  TestMockDataScenarios: ['数据', '随机 mock Case 按场景建 / 不建任务；重复扫描不重复'],
};
const checks = R.steps.flatMap((s) => s.checks);
const npass = checks.filter((c) => c.ok).length;
const gpass = gotests.filter((g) => g[0] === 'PASS').length;
const nshots = R.steps.reduce((a, s) => a + s.shots.length, 0);
const runAt = new Date(new Date(R.at).getTime() + 8 * 3600e3).toISOString().slice(0, 16).replace('T', ' ');
const commit = git('git rev-parse --short HEAD');
const branch = git('git rev-parse --abbrev-ref HEAD');
const dbDriver = process.env.E2E_DB || 'sqlite';

const groups = [
  ['SOP 1', 'sop1', 'VIP_CASE_CREATED', 'Case 创建后 RM 跟进', 'Push 建任务 → RM 跟进；延迟（No Response）到 Case 创建 + 3 天复查。'],
  ['SOP 2', 'sop2', 'VIP_ONGOING_REVIEW', 'Maker Review 超时提醒（方案 A）', '停留 ≥ 48h 提醒；满 7 天仍在 Maker Review 则在同一任务上刷新原卡片。'],
  ['运营', 'ops', '', 'Leader 队列', '需要关注、指派。'],
  ['配置', 'cfg', '', '配置端', '流程编辑器与发布前检查。'],
];
let idx = 0;
const stepHtml = (s) => {
  idx++;
  const ok = s.checks.every((c) => c.ok);
  const rows = s.checks.map((c) => `<li class="${c.ok ? 'ok' : 'bad'}"><span class="mk" aria-hidden="true">${c.ok ? '✓' : '✕'}</span><span class="cn">${e(c.name)}</span>${c.detail ? `<code class="cd">${e(c.detail)}</code>` : ''}</li>`).join('');
  const figs = s.shots.map((sh) => `<figure><a href="${e(sh.file)}" target="_blank" rel="noopener"><img src="${e(sh.file)}" alt="${e(sh.caption)}" loading="lazy"></a><figcaption>${e(sh.caption)}</figcaption></figure>`).join('');
  return `<article class="step"><header><span class="sid">${String(idx).padStart(2, '0')}</span><h3>${e(s.title)}</h3><span class="badge ${ok ? 'pass' : 'fail'}">${s.checks.filter((c) => c.ok).length}/${s.checks.length}</span></header>
  <p class="sd">${e(s.desc)}</p><ul class="checks">${rows}</ul><div class="figs n${Math.min(s.shots.length, 3)}">${figs}</div></article>`;
};
const sections = groups.map(([key, id, tkey, name, blurb]) => {
  const st = R.steps.filter((s) => s.sop === key);
  if (!st.length) return '';
  const c = st.flatMap((s) => s.checks);
  return `<section class="sop" id="${id}"><div class="sophead"><p class="eyebrow">${e(key)}${tkey ? ` · <code>${e(tkey)}</code>` : ''}</p><h2>${e(name)}</h2>
  <p class="blurb">${e(blurb)} <span class="tally">${c.filter((x) => x.ok).length}/${c.length} 断言通过</span></p></div>${st.map(stepHtml).join('')}</section>`;
}).join('');
const gorows = gotests.map(([r, n, t]) => `<tr><td><span class="tag">${e((desc[n] || [''])[0])}</span></td><td><code>${e(n)}</code><div class="gdesc">${e((desc[n] || ['', ''])[1])}</div></td><td class="num">${Number(t).toFixed(2)}s</td><td><span class="badge ${r === 'PASS' ? 'pass' : 'fail'}">${r}</span></td></tr>`).join('');
const abort = R.abort ? `<div class="panel" style="border-color:var(--bad)"><h2 style="color:var(--bad)">运行中止</h2><p>${e(R.abort)}</p></div>` : '';

const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bywork SOP 端到端测试报告</title>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=Noto+Sans+SC:wght@400;500;700&display=swap">
<style>__CSS__</style></head><body><div class="wrap">
<header><p class="eyebrow">End-to-end test report</p><h1>Bywork SOP 端到端测试报告</h1>
<div class="meta"><span>运行于 ${e(runAt)} (UTC+8)</span>${branch ? `<span>分支 <code>${e(branch)}</code></span>` : ''}${commit ? `<span>提交 <code>${e(commit)}</code></span>` : ''}<span>数据库 <code>${e(dbDriver)}</code></span></div>
<p class="lede">真实浏览器按两条 SOP Alert 的业务路径操作界面，每一步截图，并同时对页面和 API 断言；Go 端到端测试使用真实数据库、引擎与心跳。</p></header>
${abort}
<div class="tiles">
<div class="tile"><div class="n">${npass}/${checks.length}</div><div class="k">浏览器端到端断言</div><div class="h">${R.steps.length} 个场景步骤</div></div>
<div class="tile"><div class="n">${gotests.length ? `${gpass}/${gotests.length}` : '—'}</div><div class="k">Go 测试</div><div class="h">${gotests.length ? pkgs.map((p) => `${e(p[1].split('/').pop())} ${e(p[0])}`).join(' · ') : '未提供 gotest.txt'}</div></div>
<div class="tile"><div class="n">${nshots}</div><div class="k">UI 截图</div><div class="h">1440 × 900，Chromium</div></div>
<div class="tile"><div class="n">${(R.pageErrors || []).length}</div><div class="k">前端报错</div><div class="h">pageerror 与 console.error</div></div>
</div>
${sections}
${gotests.length ? `<section class="sop" id="go"><div class="sophead"><p class="eyebrow">go test -v ./...</p><h2>Go 测试套件</h2></div><div class="panel tablewrap"><table><thead><tr><th>范围</th><th>用例</th><th style="text-align:right">耗时</th><th>结果</th></tr></thead><tbody>${gorows}</tbody></table></div></section>` : ''}
</div></body></html>`;
fs.writeFileSync(path.join(OUT, 'report.html'), html.replace('__CSS__', () => css));
console.log(`report → ${path.join(OUT, 'report.html')} (${npass}/${checks.length} browser checks, ${gpass}/${gotests.length} go tests)`);
