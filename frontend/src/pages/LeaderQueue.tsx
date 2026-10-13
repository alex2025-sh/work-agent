import { useCallback, useEffect, useState } from 'react';
import { del, fmtDuration, fmtTime, get, post } from '../api';
import { useSession } from '../App';
import TaskDetail from '../components/TaskDetail';
import { Modal, RoundBadge, useRun } from '../components/ui';

interface Queue {
  team: string;
  stats: Record<string, number>;
  attention: any[];
  pending: any[];
  members: any[];
  overrides: any[];
}

export default function LeaderQueue() {
  const { refreshKey } = useSession();
  const [q, setQ] = useState<Queue | null>(null);
  const [err, setErr] = useState('');
  const [open, setOpen] = useState<number>(0);
  const [assign, setAssign] = useState<Record<number, string>>({});
  const [ovr, setOvr] = useState<{ uid: string; rm_user: string; approval_no: string; days: number } | null>(null);
  const run = useRun();

  const load = useCallback(() => {
    get<Queue>('/api/leader/queue')
      .then((x) => {
        setQ(x);
        setErr('');
      })
      .catch((e) => setErr(e.message));
  }, []);
  useEffect(load, [load, refreshKey]);

  if (err) return <div className="main"><div className="panel">{err}</div></div>;
  if (!q) return <div className="main hint">加载中…</div>;
  const rms = q.members.filter((m) => m.active);

  const reassign = async (taskId: number) => {
    const to = assign[taskId];
    if (!to) return;
    if (await run(() => post(`/api/tasks/${taskId}/reassign`, { to }), '已指派') !== undefined) load();
  };

  return (
    <div className="main">
      <div className="col" style={{ maxWidth: 1240, margin: '0 auto' }}>
        <div className="row between">
          <h1>团队队列 · {q.team}</h1>
          <span className="hint">无人处理的任务不会自动升级或改派；按放置时长查看，需要时主动提醒或改派</span>
        </div>
        <div className="row">
          {[
            ['attention', '需要关注', 'RM 缺失 · 投递失败 · 读取失败'],
            ['unassigned', '待分配', '没有处理人的待办'],
            ['processing', '处理中', '含等待下一次提醒'],
            ['closed_today', '今日结束', '处理完成或 Case 已失效'],
          ].map(([k, l, h]) => (
            <div key={k} className="panel stat grow">
              <div className="k">{l}</div>
              <div className="n" style={k === 'attention' && q.stats[k] ? { color: 'var(--red)' } : undefined}>
                {q.stats[k] || 0}
              </div>
              <div className="hint">{h}</div>
            </div>
          ))}
        </div>

        <div className="panel">
          <div className="panel-title">
            <h2>需要关注</h2>
            <span className="hint">系统无法自动推进的任务，只在这里出现，不会被随机指派</span>
          </div>
          <table className="tbl">
            <thead>
              <tr>
                <th>任务</th>
                <th>Appeal ID</th>
                <th>原因</th>
                <th>持续</th>
                <th>当前处理人</th>
                <th style={{ width: 360 }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {q.attention.length === 0 && (
                <tr>
                  <td colSpan={6} className="empty">
                    没有需要关注的任务
                  </td>
                </tr>
              )}
              {q.attention.map((a) => (
                <tr key={a.id}>
                  <td>
                    <a onClick={() => setOpen(a.id)} style={{ cursor: 'pointer' }}>
                      {a.title}
                    </a>
                  </td>
                  <td className="mono">{a.object_key}</td>
                  <td>
                    {a.reasons.map((r: string) => (
                      <span key={r} className="pill red" style={{ marginRight: 4 }}>
                        {r}
                      </span>
                    ))}
                  </td>
                  <td>{fmtDuration(a.duration_ms)}</td>
                  <td>{a.assignee_name || '—'}</td>
                  <td>
                    <div className="row gap-s">
                      <select value={assign[a.id] || ''} onChange={(e) => setAssign({ ...assign, [a.id]: e.target.value })}>
                        <option value="">选择 RM</option>
                        {rms.map((m) => (
                          <option key={m.user_id} value={m.user_id}>
                            {m.name}
                          </option>
                        ))}
                      </select>
                      <button className="small" disabled={!assign[a.id]} onClick={() => reassign(a.id)}>
                        指派
                      </button>
                      {a.attention.includes('rm_missing') && (
                        <button className="small" onClick={() => setOvr({ uid: a.uid || '', rm_user: '', approval_no: '', days: 7 })}>
                          设临时 RM
                        </button>
                      )}
                      {a.attention.includes('delivery_failed') && (
                        <button className="small" onClick={async () => (await run(() => post(`/api/tasks/${a.id}/retry-delivery`), '已重试')) !== undefined && load()}>
                          重试投递
                        </button>
                      )}
                      {(a.attention.includes('data_read_failed') || a.attention.includes('flow_error')) && (
                        <button className="small" onClick={async () => (await run(() => post(`/api/tasks/${a.id}/retrigger`), '已重新触发')) !== undefined && load()}>
                          重新触发
                        </button>
                      )}
                      <button className="small ghost" onClick={() => setOpen(a.id)}>
                        查看时间线
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="row" style={{ alignItems: 'flex-start' }}>
          <div className="panel grow">
            <div className="panel-title">
              <h2>成员负载</h2>
            </div>
            <table className="tbl">
              <thead>
                <tr>
                  <th>成员</th>
                  <th>待处理</th>
                  <th>等待提醒</th>
                  <th>放置最久</th>
                  <th>中位处理时长</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {q.members.map((m) => (
                  <tr key={m.user_id}>
                    <td>
                      {m.name} {!m.active && <span className="pill red">已停用</span>}
                    </td>
                    <td>{m.pending}</td>
                    <td>{m.waiting}</td>
                    <td>{fmtDuration(m.oldest_ms)}</td>
                    <td>{fmtDuration(m.median_done_ms)}</td>
                    <td>
                      <button className="ghost small" onClick={async () => {
                        if (m.active && !window.confirm(`将 ${m.name} 标记为离开团队？其名下待办会进入「需要关注」`)) return;
                        if (await run(() => post(`/api/members/${m.user_id}/active`, { active: !m.active }), '已更新') !== undefined) load();
                      }}>
                        {m.active ? '标记离开' : '恢复'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="panel" style={{ width: 380 }}>
            <div className="panel-title">
              <h2>临时 RM 例外</h2>
            </div>
            <div className="hint mb">优先于权威 RM 映射，需审批，到期自动失效</div>
            {q.overrides.length === 0 && <div className="hint">暂无</div>}
            {q.overrides.map((o) => (
              <div key={o.id} className="row between" style={{ padding: '6px 0', borderBottom: '1px dashed var(--line)' }}>
                <div>
                  <div className="mono">
                    UID {o.uid} → {rms.find((m) => m.user_id === o.rm_user)?.name || o.rm_user}
                  </div>
                  <div className="hint">
                    有效至 {fmtTime(o.valid_until)} · 审批 {o.approval_no}
                  </div>
                </div>
                <button className="ghost small" onClick={async () => (await run(() => del(`/api/rm-overrides/${o.id}`), '已撤销')) !== undefined && load()}>
                  撤销
                </button>
              </div>
            ))}
            <button className="small mt" onClick={() => setOvr({ uid: '', rm_user: '', approval_no: '', days: 7 })}>
              新增例外
            </button>
          </div>
        </div>

        <div className="panel">
          <div className="panel-title">
            <h2>按放置时长查看待处理</h2>
          </div>
          <table className="tbl">
            <thead>
              <tr>
                <th>任务</th>
                <th>Appeal ID</th>
                <th>处理人</th>
                <th>放置时长</th>
                <th>轮次</th>
              </tr>
            </thead>
            <tbody>
              {q.pending.map((t) => (
                <tr key={t.id} className="click" onClick={() => setOpen(t.id)}>
                  <td>
                    <RoundBadge label={t.badge} /> {t.title}
                  </td>
                  <td className="mono">{t.object_key}</td>
                  <td>{t.assignee_name}</td>
                  <td>{fmtDuration(t.placed_ms)}</td>
                  <td>第 {t.round} 轮</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {open > 0 && (
        <div className="drawer">
          <div className="row between mb">
            <h2>任务详情</h2>
            <button className="ghost" onClick={() => setOpen(0)}>
              关闭 ✕
            </button>
          </div>
          <TaskDetail id={open} onChanged={load} />
        </div>
      )}

      {ovr && (
        <Modal
          title="新增临时 RM 例外"
          onClose={() => setOvr(null)}
          footer={
            <>
              <button onClick={() => setOvr(null)}>取消</button>
              <button className="primary" onClick={async () => {
                const ok = await run(() => post('/api/rm-overrides', { uid: ovr.uid, rm_user: ovr.rm_user, approval_no: ovr.approval_no, valid_until: Date.now() + ovr.days * 86400000 }), '已新增，下一轮投递起生效');
                if (ok !== undefined) {
                  setOvr(null);
                  load();
                }
              }}>
                提交
              </button>
            </>
          }
        >
          <div className="col">
            <label className="field">
              <span>UID</span>
              <input value={ovr.uid} onChange={(e) => setOvr({ ...ovr, uid: e.target.value })} placeholder="完整 UID" />
            </label>
            <label className="field">
              <span>临时 RM</span>
              <select value={ovr.rm_user} onChange={(e) => setOvr({ ...ovr, rm_user: e.target.value })}>
                <option value="">选择</option>
                {rms.map((m) => (
                  <option key={m.user_id} value={m.user_id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>审批单号</span>
              <input value={ovr.approval_no} onChange={(e) => setOvr({ ...ovr, approval_no: e.target.value })} />
            </label>
            <label className="field">
              <span>有效天数</span>
              <input type="number" value={ovr.days} onChange={(e) => setOvr({ ...ovr, days: Number(e.target.value) })} />
            </label>
            <div className="hint">已在途的待办不会自动改派；如需立即生效请在「需要关注」中指派。</div>
          </div>
        </Modal>
      )}
    </div>
  );
}
