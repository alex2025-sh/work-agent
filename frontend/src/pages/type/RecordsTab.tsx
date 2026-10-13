import { useEffect, useState } from 'react';
import { closeReasonText, fmtDuration, fmtTime, get, post } from '../../api';
import { useSession } from '../../App';
import TaskDetail from '../../components/TaskDetail';
import { Modal, RoundBadge, StatusPill, useRun } from '../../components/ui';
import type { TypeInfo } from '../TaskTypeDetail';

function Dist({ title, data }: { title: string; data: Record<string, number> }) {
  const entries = Object.entries(data || {}).sort();
  const max = Math.max(1, ...entries.map(([, v]) => v));
  return (
    <div className="panel grow">
      <div className="hint">{title}</div>
      {entries.length === 0 && <div className="faint">—</div>}
      {entries.map(([k, v]) => (
        <div key={k} className="row gap-s" style={{ marginTop: 4 }}>
          <span style={{ width: 70 }} className="hint">
            {k}
          </span>
          <div style={{ height: 8, width: `${(v / max) * 100}%`, maxWidth: 160, background: 'var(--primary)', borderRadius: 4 }} />
          <span>{v}</span>
        </div>
      ))}
    </div>
  );
}

export default function RecordsTab({ typeKey, info }: { typeKey: string; info: TypeInfo }) {
  const { me, refreshKey } = useSession();
  const [rows, setRows] = useState<any[]>([]);
  const [m, setM] = useState<any>(null);
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState(0);
  const [manual, setManual] = useState(false);
  const [fields, setFields] = useState('{\n  "appeal_id": "",\n  "uid": ""\n}');
  const run = useRun();
  const load = () => {
    get(`/api/task-types/${typeKey}/tasks?status=${status}`).then(setRows);
    get(`/api/task-types/${typeKey}/metrics`).then(setM);
  };
  useEffect(load, [typeKey, status, refreshKey]);

  return (
    <div className="col" style={{ maxWidth: 1260, margin: '0 auto' }}>
      {m && (
        <div className="row" style={{ alignItems: 'stretch' }}>
          <div className="panel stat">
            <div className="k">误报率</div>
            <div className="n">{(m.false_positive_rate * 100).toFixed(1)}%</div>
            <div className="hint">
              "不该存在" {m.feedback.should_not_exist || 0} · "不该给我" {m.feedback.not_mine || 0}
            </div>
          </div>
          <div className="panel stat">
            <div className="k">处理时效 P50 / P90</div>
            <div className="n" style={{ fontSize: 20 }}>
              {fmtDuration(m.p50_ms)} / {fmtDuration(m.p90_ms)}
            </div>
            <div className="hint">
              共 {m.total} 个任务 · 已结束 {m.closed}
            </div>
          </div>
          <Dist title="延迟次数分布" data={m.delay_distribution} />
          <Dist title="放置时长分布（待处理）" data={m.idle_distribution} />
          <Dist title="关闭原因" data={Object.fromEntries(Object.entries(m.close_reasons || {}).map(([k, v]) => [closeReasonText[k] || k, v as number]))} />
        </div>
      )}
      <div className="panel">
        <div className="row between mb">
          <div className="seg">
            {[
              ['', '全部'],
              ['PROCESSING', '处理中'],
              ['OPEN', '等待中'],
              ['CLOSED', '已结束'],
            ].map(([k, l]) => (
              <button key={k} className={status === k ? 'on' : ''} onClick={() => setStatus(k)}>
                {l}
              </button>
            ))}
          </div>
          {me?.user.role === 'admin' && (
            <button onClick={() => setManual(true)} disabled={info.type.status !== 'enabled' && info.type.status !== 'shadow'}>
              手动补建任务
            </button>
          )}
        </div>
        <table className="tbl">
          <thead>
            <tr>
              <th>#</th>
              <th>对象</th>
              <th>标题</th>
              <th>状态</th>
              <th>处理人</th>
              <th>创建</th>
              <th>下一步</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className="click" onClick={() => setOpen(r.id)}>
                <td>{r.id}</td>
                <td className="mono">{r.object_key}</td>
                <td>
                  <RoundBadge label={r.badge} /> {r.title} {r.is_shadow && <span className="pill violet">影子</span>}
                </td>
                <td>
                  <StatusPill status={r.status} reason={r.close_reason} />
                  {r.attention?.length > 0 && <span className="pill red">需关注</span>}
                </td>
                <td>{r.assignee_name || '—'}</td>
                <td>{fmtTime(r.created_at)}</td>
                <td className="hint">{r.next_action_at ? fmtTime(r.next_action_at) : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
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
      {manual && (
        <Modal
          title="手动补建任务"
          onClose={() => setManual(false)}
          footer={
            <>
              <button onClick={() => setManual(false)}>取消</button>
              <button className="primary" onClick={async () => {
                let f: any;
                try {
                  f = JSON.parse(fields);
                } catch {
                  return;
                }
                const note = window.prompt('补建原因（进入审计）') ?? '';
                const r = await run(() => post(`/api/task-types/${typeKey}/tasks`, { fields: f, note }), '已补建');
                if (r) {
                  setManual(false);
                  load();
                }
              }}>
                创建
              </button>
            </>
          }
        >
          <div className="col">
            <div className="hint">按字段映射的"查询列"填写；去重仍然生效，资格条件只提示不拦截。</div>
            <textarea rows={10} value={fields} onChange={(e) => setFields(e.target.value)} />
          </div>
        </Modal>
      )}
    </div>
  );
}
