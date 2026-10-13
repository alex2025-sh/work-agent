import { useCallback, useEffect, useState } from 'react';
import { ApiError, attentionText, fmtTime, get, post, requestKey } from '../api';
import { useSession } from '../App';
import { CardView, Facts, LarkPreview, Modal, RField, RoundBadge, StatusPill, useRun, useToast } from './ui';

export interface Detail {
  task: any;
  type_name: string;
  type_status: string;
  view: CardView;
  fields: RField[];
  source_line: string;
  explain: { why: string[]; why_me: string[]; next: string[] };
  timeline: { at: number; kind: string; actor: string; text: string }[];
  can_act: boolean;
  can_manage: boolean;
  assignee_name: string;
  attention: string[];
  work_items: any[];
  executions?: any[];
  snapshot?: Record<string, any>;
  ai_summary: string;
}

export default function TaskDetail({ id, onChanged }: { id: number; onChanged?: () => void }) {
  const { me, refreshKey } = useSession();
  const [d, setD] = useState<Detail | null>(null);
  const [err, setErr] = useState('');
  const [pending, setPending] = useState<{ key: string; label: string; kind: string; confirm: boolean; note: boolean } | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [showLark, setShowLark] = useState(false);
  const [showExec, setShowExec] = useState(false);
  const toast = useToast();
  const run = useRun();

  const load = useCallback(() => {
    get<Detail>(`/api/tasks/${id}`)
      .then((x) => {
        setD(x);
        setErr('');
      })
      .catch((e) => setErr(e.message));
  }, [id]);
  useEffect(load, [load, refreshKey]);

  if (err) return <div className="panel">{err}</div>;
  if (!d) return <div className="panel hint">加载中…</div>;

  const t = d.task;
  const v = d.view;
  const buttons = v.card.buttons || [];
  const hasExternal = buttons.some((b) => b.kind === 'external');

  const act = async (b: { key: string; kind: string }, noteText: string) => {
    setBusy(true);
    try {
      const body = { work_item_id: v.work_item_id, button: b.key, note: noteText, request_key: requestKey(), card_rev: v.card_rev };
      if (b.kind === 'external') {
        const r = await post(`/api/tasks/${id}/open-link`, body);
        window.open(r.url, '_blank', 'noopener');
        toast(r.completed ? '已打开并完成本轮（Lark 内的提交不会回到 Bywork）' : '已打开');
      } else {
        await post(`/api/tasks/${id}/actions`, body);
        toast('已处理');
      }
      setPending(null);
      setNote('');
      load();
      onChanged?.();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === 409) {
        toast('卡片已更新，已刷新为最新内容', true);
        load();
      } else {
        toast(e.message, true);
      }
    } finally {
      setBusy(false);
    }
  };

  const click = (b: CardView['card']['buttons'][number]) => {
    if (b.confirm || b.note_required) setPending({ key: b.key, label: b.label, kind: b.kind, confirm: b.confirm, note: b.note_required });
    else act(b, '');
  };

  const feedback = async (kind: string) => {
    const n = window.prompt(kind === 'not_mine' ? '为什么不该给你？（可选）' : '为什么这个任务不该存在？（可选）') ?? null;
    if (n === null) return;
    await run(() => post(`/api/tasks/${id}/feedback`, { kind, note: n }), '已反馈给配置人员');
    load();
  };

  const statusPill =
    v.state === 'actionable' && d.can_act ? (
      <span className="pill amber">待你处理</span>
    ) : v.state === 'unassigned' ? (
      <span className="pill red">待指派</span>
    ) : (
      <StatusPill status={t.status} reason={t.close_reason} />
    );

  return (
    <div className="col">
      <div className="panel section-gap">
        <div className="row between">
          <div className="row gap-s">
            <RoundBadge label={v.card.round_label} />
            <h1>{v.card.title || d.type_name}</h1>
            {t.is_shadow === 1 && <span className="pill violet">影子运行</span>}
          </div>
          {statusPill}
        </div>
        <div className="hint">
          Task #{t.id} · {t.type_key} 配置 v{t.config_version} · 流程 v{t.flow_version} · 卡片 r{v.card_rev}
          {v.card.round > 1 && ` · 第 ${v.card.round} 轮`} · 处理人 {d.assignee_name || '未分配'}
        </div>
        {d.attention.length > 0 && (
          <div className="row gap-s">
            {d.attention.map((a) => (
              <span key={a} className="pill red">
                {attentionText[a] || a}
              </span>
            ))}
          </div>
        )}
        <Facts fields={d.fields} />
        <div className="hint">{d.source_line}</div>
        {v.card.explain !== false && (
          <div className="explain">
            <div>
              <h4>为什么有这个任务</h4>
              {d.explain.why.map((x, i) => (
                <p key={i}>{x}</p>
              ))}
            </div>
            <div>
              <h4>为什么是我</h4>
              {d.explain.why_me.length ? d.explain.why_me.map((x, i) => <p key={i}>{x}</p>) : <p className="faint">—</p>}
            </div>
            <div className="next">
              <h4>接下来会发生什么</h4>
              {d.explain.next.map((x, i) => (
                <p key={i}>{x}</p>
              ))}
            </div>
          </div>
        )}
        {v.card.ai?.enabled && (
          <div className="ai">
            <div className="hint">AI 摘要 · 仅供参考，以上方事实为准</div>
            {d.ai_summary || <span className="faint">摘要暂不可用（失败时只隐藏摘要，事实字段照常展示）</span>}
          </div>
        )}
        {v.previous && <div className="hint">上一轮：{v.previous}</div>}
        {(v.result || v.waiting) && (
          <div className="ai" style={{ borderStyle: 'solid' }}>
            {v.result && <div>{v.result}</div>}
            {v.waiting && <div className="hint">{v.waiting}</div>}
          </div>
        )}
        {v.state === 'actionable' && (
          <div className="row wrap">
            {buttons.map((b) => (
              <button key={b.key} className={b.style === 'primary' ? 'primary' : b.style === 'danger' ? 'danger' : ''} disabled={!d.can_act || busy} onClick={() => click(b)}>
                {b.label}
              </button>
            ))}
            {hasExternal && <span className="hint">打开 Lark 只记录外跳；Lark 内的提交与 Compliance 结果不会回到这里</span>}
            {!d.can_act && <span className="hint">只有当前处理人可以操作</span>}
          </div>
        )}
        <div className="row wrap">
          {v.card.links?.map((l) => (
            <a key={l.url} href={l.url} target="_blank" rel="noreferrer">
              {l.label} ↗
            </a>
          ))}
          <span className="spacer grow" />
          <button className="ghost small" onClick={() => feedback('not_mine')}>
            这个任务不该给我
          </button>
          <button className="ghost small" onClick={() => feedback('should_not_exist')}>
            不该存在？反馈给配置人员
          </button>
          <button className="ghost small" onClick={() => setShowLark(!showLark)}>
            {showLark ? '隐藏' : '查看'} Lark 卡片
          </button>
        </div>
        {showLark && <LarkPreview view={v} typeName={d.type_name} />}
      </div>

      {d.can_manage && <LeaderActions d={d} reload={() => { load(); onChanged?.(); }} />}

      <div className="panel">
        <div className="panel-title">
          <h2>时间线</h2>
        </div>
        <ul className="timeline">
          {d.timeline.map((x, i) => (
            <li key={i}>
              <span className="t">{fmtTime(x.at)}</span>
              <span>{x.text}</span>
            </li>
          ))}
        </ul>
        {d.executions && d.executions.length > 0 && (
          <div className="mt">
            <button className="ghost small" onClick={() => setShowExec(!showExec)}>
              {showExec ? '隐藏' : '查看'}节点执行记录（{d.executions.length}）
            </button>
            {showExec && (
              <table className="tbl mt">
                <thead>
                  <tr>
                    <th>#</th>
                    <th>节点</th>
                    <th>状态</th>
                    <th>出口</th>
                    <th>开始</th>
                    <th>耗时</th>
                    <th>错误</th>
                  </tr>
                </thead>
                <tbody>
                  {d.executions.map((e: any) => (
                    <tr key={e.id}>
                      <td>{e.id}</td>
                      <td className="mono">{e.node_key}</td>
                      <td>{e.status}</td>
                      <td className="mono">{e.exit}</td>
                      <td>{fmtTime(e.started_at)}</td>
                      <td>{e.duration_ms}ms</td>
                      <td className="hint">{e.error}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        )}
      </div>

      {pending && (
        <Modal
          title={`确认「${pending.label}」`}
          onClose={() => setPending(null)}
          footer={
            <>
              <button onClick={() => setPending(null)}>取消</button>
              <button className="primary" disabled={busy || (pending.note && !note.trim())} onClick={() => act(pending, note)}>
                确认
              </button>
            </>
          }
        >
          <div className="col">
            <div>
              {v.card.title} · {d.fields.find((f) => f.field.endsWith('appeal_id'))?.value}
            </div>
            {pending.kind === 'external' && <div className="hint">将打开外部流程；Bywork 只记录"已打开"，不代表外部已提交。</div>}
            <label className="field">
              <span>备注{pending.note ? '（必填）' : '（可选）'}</span>
              <textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
            </label>
          </div>
        </Modal>
      )}
      {me?.user.role === 'admin' && d.snapshot && (
        <details className="panel">
          <summary className="hint">运行变量（管理员）</summary>
          <pre className="diff">{JSON.stringify(d.snapshot, null, 2)}</pre>
        </details>
      )}
    </div>
  );
}

function LeaderActions({ d, reload }: { d: Detail; reload: () => void }) {
  const [members, setMembers] = useState<any[]>([]);
  const [to, setTo] = useState('');
  const run = useRun();
  useEffect(() => {
    get('/api/members?team=' + d.task.team).then((ms) => setMembers(ms.filter((m: any) => m.role === 'rm' && m.active)));
  }, [d.task.team]);
  const t = d.task;
  const closed = t.status === 'CLOSED';
  return (
    <div className="panel">
      <div className="panel-title">
        <h2>Leader 操作</h2>
        <span className="hint">无人处理不会自动升级或改派；所有操作进入审计</span>
      </div>
      <div className="row wrap">
        <select value={to} onChange={(e) => setTo(e.target.value)} disabled={closed}>
          <option value="">改派 / 指派给…</option>
          {members.map((m) => (
            <option key={m.user_id} value={m.user_id}>
              {m.name}
            </option>
          ))}
        </select>
        <button disabled={!to || closed} onClick={async () => {
          const note = window.prompt('改派原因（可选）') ?? '';
          if (await run(() => post(`/api/tasks/${t.id}/reassign`, { to, note }), '已改派') !== undefined) reload();
        }}>
          改派
        </button>
        <button disabled={closed} onClick={async () => {
          if (await run(() => post(`/api/tasks/${t.id}/retrigger`), '已重新触发') !== undefined) reload();
        }}>
          重新触发
        </button>
        <button disabled={closed} onClick={async () => {
          if (await run(() => post(`/api/tasks/${t.id}/retry-delivery`), '已重新投递') !== undefined) reload();
        }}>
          重试投递
        </button>
        <button className="danger" disabled={closed} onClick={async () => {
          const note = window.prompt('取消原因');
          if (note === null) return;
          if (await run(() => post(`/api/tasks/${t.id}/cancel`, { note }), '任务已取消') !== undefined) reload();
        }}>
          取消任务
        </button>
      </div>
    </div>
  );
}
