// A stand-in for the internal OpenClaw service, for the AI-assistant e2e run.
//
// It speaks the same protocol Bywork uses against OpenClaw: POST
// /v1/chat/completions with stream=true and client function tools; answers are
// SSE chunks with text deltas or tool_calls (split into fragments, like a real
// model), ending with `data: [DONE]`. The "model" is a small script driven by
// the last user message and the tool results that followed it, so the browser
// run exercises the real HTTP path, Bywork's tool loop and validation.
//
//   PORT=18891 TOKEN=e2e-gateway-token node fake-openclaw.mjs
//
// GET /__log returns every request received (for assertions); POST /__reset clears it.
import http from 'node:http';

const PORT = Number(process.env.PORT || 18891);
const TOKEN = process.env.TOKEN || 'e2e-gateway-token';
const log = [];
let callSeq = 0;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function toolResult(m) {
  try {
    return JSON.parse(m.content).bywork_tool_result;
  } catch {
    return null;
  }
}

/** The script: returns {text} or {tool, args}. */
function decide(messages) {
  let last = -1;
  messages.forEach((m, i) => m.role === 'user' && (last = i));
  const ask = last >= 0 ? messages[last].content : '';
  const results = messages.slice(last + 1).filter((m) => m.role === 'tool').map((m) => ({ name: m.name, r: toolResult(m) }));
  const got = (name) => results.filter((x) => x.name === name);
  const ctx = got('get_context')[0]?.r;

  if (ask.includes('触发模型错误')) return { error: 'upstream model overloaded (simulated)' };

  // 1) 采集配置：资格门槛时长
  const dur = ask.match(/(\d+)\s*小时.*?(?:改成|改为|调到)\s*(\d+)\s*小时/);
  if (dur) {
    if (!ctx) return { tool: 'get_context', args: {} };
    const prop = got('propose_config')[0]?.r;
    if (!prop) {
      const spec = structuredClone(ctx.config.spec);
      let n = 0;
      for (const r of spec.eligibility.all || []) {
        if (r.op === 'elapsed_gte' && r.value === `${dur[1]}h`) {
          r.value = `${dur[2]}h`;
          r.label = `Maker Review ≥ ${dur[2]}h`;
          n++;
        }
      }
      if (!n) return { text: `资格条件里没有 ${dur[1]}h 的时长条件，没有改动。` };
      return { tool: 'propose_config', args: { summary: `资格门槛 ${dur[1]}h → ${dur[2]}h`, spec } };
    }
    return { text: summarize(prop, `把资格条件「Maker Review ≥ ${dur[1]}h」改为 ≥ ${dur[2]}h，其他配置不变。`) };
  }

  // 2) 处理流程：7 天提醒前再确认一次 VIP 身份
  if (ask.includes('确认') && ask.includes('VIP')) {
    if (!ctx) return { tool: 'get_context', args: {} };
    if (!got('list_node_types').length) return { tool: 'list_node_types', args: {} };
    const tries = got('propose_flow');
    const ops = [
      { op: 'add_node', key: 'vip_recheck', type: 'data', name: '再次确认 VIP 身份', after: 'still', config: { capability: 'data.vip.profile', args: { uid: '{{case.uid}}' }, on_error: 'retry', retries: 3, backoff: '5m' } },
      { op: 'add_node', key: 'vip_check', type: 'condition', name: '仍是 VIP？', after: 'vip_recheck', config: { branches: [{ exit: 'yes', label: '仍是 VIP', when: { all: [{ field: 'vip.is_vip', op: 'is_true', label: '用户仍是 VIP' }] } }], default_exit: 'no' } },
      { op: 'add_node', key: 'end_not_vip', type: 'end', name: '结束 · 已不是 VIP', after: 'vip_check', config: { close_reason: 'invalidated' } },
      { op: 'connect', from: 'still', exit: 'yes', to: 'vip_recheck' },
      { op: 'connect', from: 'vip_recheck', exit: 'next', to: 'vip_check' },
      { op: 'connect', from: 'vip_check', exit: 'yes', to: 'rm_7d' },
    ];
    // first attempt forgets the "no" exit, like a model sometimes does; Bywork's validation catches it
    if (tries.length === 0) return { tool: 'propose_flow', args: { summary: '7 天提醒前再确认 VIP 身份', ops } };
    if (tries.length === 1 && !tries[0].r?.validation?.ok) {
      return { tool: 'propose_flow', args: { summary: '7 天提醒前再确认 VIP 身份；已不是 VIP 则以失效结束', ops: [...ops, { op: 'connect', from: 'vip_check', exit: 'no', to: 'end_not_vip' }] } };
    }
    return { text: summarize(tries[tries.length - 1].r, '在「Case 仍在 Maker Review？」之后新增「再次确认 VIP 身份」取数节点和「仍是 VIP？」判断：仍是 VIP 才发 7 天提醒，否则以「已不是 VIP」失效结束。第一次提交漏连了「否」出口，已按校验结果补上。') };
  }

  // 3) 卡片字段
  const card = ask.match(/卡片.*?(?:加上|增加|加)\s*([^\s（(]+)?\s*[（(]?\s*([a-z_]+\.[a-z_]+)/);
  if (ask.includes('卡片') && card) {
    if (!ctx) return { tool: 'get_context', args: {} };
    const field = card[2];
    const label = (card[1] || field).replace(/[，,。]/g, '');
    const prop = got('propose_flow')[0]?.r;
    if (!prop) {
      const cards = typeof ctx.flow.cards === 'string' ? JSON.parse(ctx.flow.cards) : ctx.flow.cards;
      const key = Object.keys(cards).sort()[0];
      const tpl = structuredClone(cards[key]);
      tpl.fields.push({ field, label, format: 'text', lark: true, list: false, missing: '—' });
      return { tool: 'propose_flow', args: { summary: `卡片 ${key} 增加「${label}」`, ops: [{ op: 'set_card', key, template: tpl }] } };
    }
    if (!prop.validation?.ok) {
      return { text: `没法直接加：${prop.validation.errors.join('；')}。需要先在采集配置里增加 ${field} 的字段映射（数据源里要有对应的列），要我先看看表结构吗？` };
    }
    return { text: summarize(prop, `卡片增加字段「${label}」（${field}）。`) };
  }

  // 4) 看样本数据
  if (ask.includes('样本')) {
    if (!got('describe_table').length) return { tool: 'describe_table', args: { datasource: 'demo', table: 'demo_case' } };
    if (!got('sample_query').length) return { tool: 'sample_query', args: { datasource: 'demo', query: "SELECT c.appeal_id, c.uid, c.stage, c.stage_entered_at, v.six_month_profit FROM demo_case c JOIN demo_vip v ON v.uid = c.uid WHERE c.stage = 'maker_review'" } };
    const s = got('sample_query')[0].r;
    return { text: `demo_case 有 ${got('describe_table')[0].r.columns.length} 列；试跑返回 ${s.row_count} 行，uid、盈利等敏感列已脱敏。` };
  }

  return { text: '我可以帮你修改采集配置（如资格条件、字段映射）和处理流程（节点、分支、卡片）。请描述要改什么。' };
}

function summarize(prop, what) {
  if (!prop?.validation?.ok) return `提议没有通过校验：${(prop?.validation?.errors || []).join('；')}`;
  const warn = prop.validation.warnings?.length ? `提示：${prop.validation.warnings.join('；')}。` : '';
  return `${what}已生成提议 #${prop.proposal_id}，校验通过。${warn}请在提议卡片里查看 diff，确认后点「接受到草稿」；发布需要单独进行。`;
}

async function stream(res, out) {
  res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' });
  const send = (obj) => res.write(`data: ${JSON.stringify(obj)}\n\n`);
  const chunk = (delta, finish = null) => send({ id: 'chatcmpl-e2e', object: 'chat.completion.chunk', model: 'openclaw/bywork', choices: [{ index: 0, delta, finish_reason: finish }] });
  if (out.error) {
    send({ error: { message: out.error, type: 'server_error' } });
  } else if (out.tool) {
    const id = `call_${++callSeq}`;
    const args = JSON.stringify(out.args);
    chunk({ role: 'assistant', tool_calls: [{ index: 0, id, type: 'function', function: { name: out.tool, arguments: '' } }] });
    for (let i = 0; i < args.length; i += 40) {
      chunk({ tool_calls: [{ index: 0, function: { arguments: args.slice(i, i + 40) } }] });
      if (i % 400 === 0) await sleep(5);
    }
    chunk({}, 'tool_calls');
  } else {
    chunk({ role: 'assistant', content: '' });
    const t = [...out.text];
    for (let i = 0; i < t.length; i += 6) {
      chunk({ content: t.slice(i, i + 6).join('') });
      await sleep(15);
    }
    chunk({}, 'stop');
  }
  res.write('data: [DONE]\n\n');
  res.end();
}

http
  .createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/__log') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify(log));
    }
    if (req.method === 'POST' && req.url === '/__reset') {
      log.length = 0;
      res.writeHead(204);
      return res.end();
    }
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') {
      res.writeHead(404);
      return res.end();
    }
    let body = '';
    for await (const c of req) body += c;
    const auth = req.headers.authorization || '';
    let parsed = null;
    try {
      parsed = JSON.parse(body);
    } catch {
      /* fallthrough */
    }
    log.push({ at: Date.now(), auth, body: parsed });
    if (auth !== `Bearer ${TOKEN}`) {
      res.writeHead(401, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ error: { message: 'invalid gateway token' } }));
    }
    if (!parsed || !Array.isArray(parsed.messages)) {
      res.writeHead(400);
      return res.end();
    }
    await stream(res, decide(parsed.messages));
  })
  .listen(PORT, '127.0.0.1', () => console.log(`fake OpenClaw on :${PORT}`));
