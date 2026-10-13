import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, currentUser, fmtTime, get, post } from '../api';
import { Modal, useRun } from './ui';
import { contextDiff } from './diff';

// AI 配置助手：对话 → 模型调用 Bywork 工具 → 生成「提议」→ 人看 diff 后接受到草稿。
// 模型不能保存草稿、不能发布；接受时后端会检查草稿 revision 未变化。

interface Status {
  enabled: boolean;
  provider: string;
  model: string;
  scripted: boolean;
  sample_rows: string;
  prompt_version: string;
}

interface Validation {
  ok: boolean;
  errors: string[];
  warnings: string[];
  info: string[];
}

export interface Proposal {
  id: number;
  kind: 'config' | 'flow';
  summary: string;
  status: string;
  ok: number;
  base_revision: number;
  created_at: number;
  validation: Validation;
  payload?: any;
}

interface ToolChip {
  id: string;
  name: string;
  phase: string;
  ok: boolean;
  note?: string;
}

type Item =
  | { k: 'user'; text: string; at: number }
  | { k: 'assistant'; text: string; at: number }
  | { k: 'tool'; tool: ToolChip; at: number }
  | { k: 'proposal'; id: number; at: number }
  | { k: 'error'; text: string; at: number };

const toolNames: Record<string, string> = {
  get_context: '读取当前草稿',
  list_node_types: '查看节点类型',
  list_capabilities: '查看取数能力',
  describe_table: '查看表结构',
  sample_query: '试运行 SQL',
  propose_config: '提交采集配置提议',
  propose_flow: '提交流程提议',
};

const statusText: Record<string, string> = {
  pending: '待确认',
  accepted: '已接受到草稿',
  rejected: '已拒绝',
  superseded: '已被新提议替代',
  stale: '草稿已变化，已失效',
};

function itemsFromSession(v: any): { items: Item[]; proposals: Record<number, Proposal> } {
  const items: Item[] = [];
  for (const m of v.messages || []) {
    if (m.role === 'user') items.push({ k: 'user', text: m.content, at: m.created_at });
    else if (m.role === 'assistant') {
      if (m.content) items.push({ k: 'assistant', text: m.content, at: m.created_at });
      for (const c of m.tool_calls || []) items.push({ k: 'tool', tool: { id: c.id, name: c.function.name, phase: 'done', ok: true }, at: m.created_at });
    }
  }
  const proposals: Record<number, Proposal> = {};
  for (const p of v.proposals || []) {
    proposals[p.id] = p;
    items.push({ k: 'proposal', id: p.id, at: p.created_at });
  }
  items.sort((a, b) => a.at - b.at);
  return { items, proposals };
}

export default function AIAssistant({ typeKey, onClose, onAccepted }: { typeKey: string; onClose: () => void; onAccepted: () => void }) {
  const run = useRun();
  const [status, setStatus] = useState<Status | null>(null);
  const [sessions, setSessions] = useState<any[]>([]);
  const [sid, setSid] = useState<string>('');
  const [items, setItems] = useState<Item[]>([]);
  const [proposals, setProposals] = useState<Record<number, Proposal>>({});
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [viewing, setViewing] = useState<number | null>(null);
  const [err, setErr] = useState('');
  const bottom = useRef<HTMLDivElement>(null);

  const loadSession = useCallback(async (id: string) => {
    const v = await get(`/api/ai/sessions/${id}`);
    const r = itemsFromSession(v);
    setItems(r.items);
    setProposals(r.proposals);
    setSid(id);
  }, []);

  useEffect(() => {
    get<Status>('/api/ai/status').then(async (s) => {
      setStatus(s);
      if (!s.enabled) return;
      try {
        const list = await get<any[]>(`/api/task-types/${typeKey}/ai/sessions`);
        setSessions(list);
        if (list.length) await loadSession(list[0].id);
      } catch (e: any) {
        setErr(e.message);
      }
    });
  }, [typeKey, loadSession]);

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: 'end' });
  }, [items]);

  const newSession = async () => {
    const s = await run(() => post(`/api/task-types/${typeKey}/ai/sessions`));
    if (!s) return null;
    setSessions((l) => [s, ...l]);
    setSid(s.id);
    setItems([]);
    setProposals({});
    return s.id as string;
  };

  const send = async (msg: string) => {
    msg = msg.trim();
    if (!msg || busy) return;
    let id = sid;
    if (!id) {
      const n = await newSession();
      if (!n) return;
      id = n;
    }
    setText('');
    setBusy(true);
    const now = Date.now();
    setItems((l) => [...l, { k: 'user', text: msg, at: now }]);
    let open = false; // an assistant bubble is receiving deltas
    const push = (it: Item) => setItems((l) => [...l, it]);
    try {
      const res = await fetch(`/api/ai/sessions/${id}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream', 'X-Bywork-User': currentUser() },
        body: JSON.stringify({ text: msg }),
      });
      if (!res.ok || !res.body) {
        let m = `HTTP ${res.status}`;
        try {
          m = (await res.json()).msg || m;
        } catch {
          /* ignore */
        }
        throw new ApiError(res.status, m);
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const line = block.split('\n').find((l) => l.startsWith('data:'));
          if (!line) continue;
          const ev = JSON.parse(line.slice(5));
          const at = Date.now();
          if (ev.type === 'delta') {
            if (open) {
              setItems((l) => {
                const last = l[l.length - 1];
                return last && last.k === 'assistant' ? [...l.slice(0, -1), { ...last, text: last.text + ev.text }] : [...l, { k: 'assistant', text: ev.text, at }];
              });
            } else {
              push({ k: 'assistant', text: ev.text, at });
              open = true;
            }
          } else if (ev.type === 'tool') {
            open = false;
            if (ev.tool.phase === 'start') push({ k: 'tool', tool: ev.tool, at });
            else setItems((l) => l.map((x) => (x.k === 'tool' && x.tool.id === ev.tool.id ? { ...x, tool: ev.tool } : x)));
          } else if (ev.type === 'proposal') {
            open = false;
            setProposals((m) => ({ ...m, [ev.proposal.id]: ev.proposal }));
            push({ k: 'proposal', id: ev.proposal.id, at });
          } else if (ev.type === 'error') {
            open = false;
            push({ k: 'error', text: ev.message, at });
          }
        }
      }
    } catch (e: any) {
      push({ k: 'error', text: e.message, at: Date.now() });
    } finally {
      setBusy(false);
      // the session list title and proposal statuses come from the server
      get<any[]>(`/api/task-types/${typeKey}/ai/sessions`).then(setSessions).catch(() => {});
    }
  };

  const decide = async (p: Proposal, action: 'accept' | 'reject') => {
    try {
      const r = await post<Proposal>(`/api/ai/proposals/${p.id}/${action}`);
      setProposals((m) => ({ ...m, [p.id]: { ...m[p.id], ...r } }));
      setViewing(null);
      if (action === 'accept') onAccepted();
      return true;
    } catch (e: any) {
      if (e instanceof ApiError && e.code === 409) {
        const fresh = await get<Proposal>(`/api/ai/proposals/${p.id}`).catch(() => null);
        if (fresh) setProposals((m) => ({ ...m, [p.id]: { ...m[p.id], status: fresh.status } }));
      }
      run(() => Promise.reject(e));
      return false;
    }
  };

  return (
    <div className="ai-drawer" role="dialog" aria-label="AI 配置助手">
      <div className="ai-head">
        <div className="row gap-s">
          <strong>AI 配置助手</strong>
          {status?.enabled && <span className={`pill ${status.scripted ? 'amber' : 'green'}`}>{status.model}</span>}
        </div>
        <div className="row gap-s">
          {status?.enabled && sessions.length > 0 && (
            <select value={sid} onChange={(e) => loadSession(e.target.value)} aria-label="历史会话">
              {sessions.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.title || '新会话'} · {fmtTime(s.updated_at)}
                </option>
              ))}
            </select>
          )}
          {status?.enabled && (
            <button className="ghost" onClick={newSession} disabled={busy}>
              新会话
            </button>
          )}
          <button className="ghost" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </div>
      </div>

      {status && !status.enabled ? (
        <div className="ai-body">
          <div className="hint">AI 配置助手未启用。在 etc/bywork.yaml 中配置 <code>AI.Enabled: true</code> 和 OpenClaw 地址后重启服务。</div>
        </div>
      ) : (
        <>
          <div className="ai-body">
            {err && <div className="ai-err">{err}</div>}
            {items.length === 0 && (
              <div className="ai-empty">
                <div>用自然语言描述要改的采集配置或处理流程。助手会读取当前草稿、生成修改提议并做校验；你确认 diff 后才写入草稿，发布仍需单独操作。</div>
                <div className="hint" style={{ marginTop: 10 }}>
                  试试：
                </div>
                {['把 48 小时改成 72 小时', '卡片加 case.stage（当前阶段）'].map((s) => (
                  <button key={s} className="ai-suggest" onClick={() => send(s)} disabled={busy}>
                    {s}
                  </button>
                ))}
                {status?.scripted && <div className="hint" style={{ marginTop: 10 }}>当前为脚本模拟模式（未接入模型），只识别上面两类示例。</div>}
              </div>
            )}
            {items.map((it, i) => {
              switch (it.k) {
                case 'user':
                  return (
                    <div key={i} className="ai-msg user">
                      {it.text}
                    </div>
                  );
                case 'assistant':
                  return (
                    <div key={i} className="ai-msg assistant">
                      {it.text}
                    </div>
                  );
                case 'tool':
                  return (
                    <div key={i} className={`ai-tool ${it.tool.phase === 'start' ? 'running' : it.tool.ok ? 'ok' : 'bad'}`}>
                      {it.tool.phase === 'start' ? '…' : it.tool.ok ? '✓' : '✕'} {toolNames[it.tool.name] || it.tool.name}
                      {it.tool.note && <span className="hint"> · {it.tool.note}</span>}
                    </div>
                  );
                case 'proposal': {
                  const p = proposals[it.id];
                  return p ? <ProposalCard key={i} p={p} onView={() => setViewing(p.id)} onDecide={decide} /> : null;
                }
                case 'error':
                  return (
                    <div key={i} className="ai-err">
                      {it.text}
                    </div>
                  );
              }
              return null;
            })}
            {busy && <div className="hint">助手处理中…</div>}
            <div ref={bottom} />
          </div>
          <form
            className="ai-input"
            onSubmit={(e) => {
              e.preventDefault();
              send(text);
            }}
          >
            <textarea
              value={text}
              rows={2}
              placeholder="例如：把资格条件的 48 小时改成 72 小时"
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  send(text);
                }
              }}
              disabled={busy}
              aria-label="消息"
            />
            <button className="primary" type="submit" disabled={busy || !text.trim()}>
              发送
            </button>
          </form>
        </>
      )}
      {viewing !== null && proposals[viewing] && <ProposalDiff p={proposals[viewing]} onClose={() => setViewing(null)} onDecide={decide} />}
    </div>
  );
}

function ProposalCard({ p, onView, onDecide }: { p: Proposal; onView: () => void; onDecide: (p: Proposal, a: 'accept' | 'reject') => void }) {
  const v = p.validation || { ok: false, errors: [], warnings: [], info: [] };
  const pending = p.status === 'pending';
  return (
    <div className={`ai-proposal ${p.status}`}>
      <div className="row between">
        <strong>
          提议 #{p.id} · {p.kind === 'config' ? '采集配置' : '处理流程'}
        </strong>
        <span className={`pill ${p.status === 'accepted' ? 'green' : pending ? (p.ok ? 'blue' : 'red') : ''}`}>{pending && !p.ok ? '校验未通过' : statusText[p.status] || p.status}</span>
      </div>
      <div style={{ margin: '6px 0' }}>{p.summary}</div>
      <div className="hint">基于草稿 r{p.base_revision}</div>
      {(v.errors || []).map((e, i) => (
        <div key={'e' + i} className="ai-v err">
          ✕ {e}
        </div>
      ))}
      {(v.warnings || []).map((e, i) => (
        <div key={'w' + i} className="ai-v warn">
          ! {e}
        </div>
      ))}
      {(v.info || []).slice(0, 3).map((e, i) => (
        <div key={'i' + i} className="ai-v info">
          · {e}
        </div>
      ))}
      <div className="row gap-s" style={{ marginTop: 8 }}>
        <button onClick={onView}>查看 diff</button>
        {pending && p.ok === 1 && (
          <button className="primary" onClick={() => onDecide(p, 'accept')}>
            接受到草稿
          </button>
        )}
        {pending && <button onClick={() => onDecide(p, 'reject')}>拒绝</button>}
      </div>
    </div>
  );
}

const pretty = (v: any) => JSON.stringify(v ?? null, null, 2);

function ProposalDiff({ p, onClose, onDecide }: { p: Proposal; onClose: () => void; onDecide: (p: Proposal, a: 'accept' | 'reject') => Promise<boolean> }) {
  const [full, setFull] = useState<Proposal | null>(null);
  useEffect(() => {
    get<Proposal>(`/api/ai/proposals/${p.id}`).then(setFull);
  }, [p.id]);
  const pl = full?.payload;
  let a = '';
  let b = '';
  if (pl && p.kind === 'config') {
    a = pretty(pl.before);
    b = pretty(pl.after);
  } else if (pl) {
    // layout coordinates are left out, like the versions tab
    a = pretty({ graph: pl.before?.graph, cards: pl.before?.cards });
    b = pretty({ graph: pl.after?.graph, cards: pl.after?.cards });
  }
  const ch = pl?.change;
  const pending = p.status === 'pending';
  return (
    <Modal
      title={`提议 #${p.id} · ${p.summary}`}
      onClose={onClose}
      width={820}
      footer={
        <>
          <button onClick={onClose}>关闭</button>
          {pending && <button onClick={() => onDecide(p, 'reject')}>拒绝</button>}
          {pending && p.ok === 1 && (
            <button className="primary" onClick={() => onDecide(p, 'accept')}>
              接受到草稿
            </button>
          )}
        </>
      }
    >
      {!pl ? (
        <div className="hint">加载中…</div>
      ) : (
        <div className="col">
          {ch && (
            <div className="ai-change">
              {(
                [
                  ['新增节点', ch.added_nodes],
                  ['修改节点', ch.changed_nodes],
                  ['删除节点', ch.removed_nodes],
                  ['新增连线', ch.added_edges],
                  ['删除连线', ch.removed_edges],
                  ['卡片变更', ch.changed_cards],
                ] as [string, string[]][]
              )
                .filter(([, l]) => l && l.length)
                .map(([t, l]) => (
                  <div key={t}>
                    <span className="hint">{t}：</span>
                    {l.map((x) => (
                      <code key={x} style={{ marginRight: 6 }}>
                        {x}
                      </code>
                    ))}
                  </div>
                ))}
            </div>
          )}
          <div className="hint">接受后写入草稿（需草稿仍为 r{p.base_revision}）；发布在「发布…」中单独进行。</div>
          <div className="diff">
            {contextDiff(a, b).map((l, i) => (
              <span key={i} className={l.t === '+' ? 'add' : l.t === '-' ? 'rm' : ''}>
                {l.t} {l.s}
              </span>
            ))}
          </div>
        </div>
      )}
    </Modal>
  );
}
