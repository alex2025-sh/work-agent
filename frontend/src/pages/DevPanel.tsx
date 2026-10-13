import { useEffect, useState } from 'react';
import { fmtTime, get, post } from '../api';
import { useSession } from '../App';
import { useRun } from '../components/ui';

/** Demo console (dev mode only): simulate CRM, move time, inspect Lark cards. */
export default function DevPanel() {
  const { refreshKey, bump } = useSession();
  const [cases, setCases] = useState<any[]>([]);
  const [lark, setLark] = useState<any[]>([]);
  const [nc, setNc] = useState({ appeal_id: '', uid: '8812003390', case_type: 'withdrawal_appeal' });
  const run = useRun();
  const load = () => {
    get('/api/dev/cases').then(setCases);
    get('/api/dev/lark').then((l) => setLark((l || []).sort((a: any, b: any) => (a.at < b.at ? 1 : -1))));
  };
  useEffect(load, [refreshKey]);
  const upd = async (id: string, body: any, ok: string) => {
    const r = await run(() => post(`/api/dev/cases/${id}/update`, body), ok);
    if (r) {
      load();
      bump();
    }
  };
  return (
    <div className="main">
      <div className="col" style={{ maxWidth: 1260, margin: '0 auto' }}>
        <div>
          <h1>演示台</h1>
          <div className="hint">
            模拟 CRM 与 Lark：用右上角的 +1h / +1d / +3d 推进系统时间（会同时执行三个心跳），观察 T+3 复查、48h → 7 天刷新同一张卡片、Case 失效自动关闭等行为。
          </div>
        </div>
        <div className="panel">
          <div className="panel-title">
            <h2>新建 Case（触发业务 Push → VIP_CASE_CREATED）</h2>
          </div>
          <div className="row wrap">
            <input placeholder="Appeal ID，如 AP-20261013-0999" value={nc.appeal_id} onChange={(e) => setNc({ ...nc, appeal_id: e.target.value })} />
            <input placeholder="UID" value={nc.uid} onChange={(e) => setNc({ ...nc, uid: e.target.value })} />
            <select value={nc.case_type} onChange={(e) => setNc({ ...nc, case_type: e.target.value })}>
              {['withdrawal_appeal', 'account_appeal', 'trading_appeal', 'other'].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
            <button className="primary" disabled={!nc.appeal_id} onClick={async () => {
              const r = await run(() => post('/api/dev/cases', nc));
              if (r) {
                load();
                bump();
              }
            }}>
              创建并推送
            </button>
          </div>
        </div>
        <div className="panel">
          <div className="panel-title">
            <h2>CRM Case（demo_case）</h2>
          </div>
          <table className="tbl">
            <thead>
              <tr>
                <th>Appeal ID</th>
                <th>UID</th>
                <th>类型</th>
                <th>状态</th>
                <th>阶段</th>
                <th>进入阶段</th>
                <th>创建</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {cases.map((c) => (
                <tr key={c.appeal_id}>
                  <td className="mono">{c.appeal_id}</td>
                  <td className="mono">{c.uid}</td>
                  <td>{c.case_type}</td>
                  <td>{c.status === 'open' ? <span className="pill green">open</span> : <span className="pill">{c.status}</span>}</td>
                  <td>{c.stage}</td>
                  <td>{fmtTime(c.stage_entered_at)}</td>
                  <td>{fmtTime(c.created_at)}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    {c.stage !== 'maker_review' && (
                      <button className="small" onClick={() => upd(c.appeal_id, { stage: 'maker_review' }, '已进入 Maker Review')}>
                        进入 Maker Review
                      </button>
                    )}
                    {c.stage === 'maker_review' && (
                      <button className="small" onClick={() => upd(c.appeal_id, { stage: 'checker_review' }, '已离开 Maker Review')}>
                        离开 Maker Review
                      </button>
                    )}{' '}
                    {c.status === 'open' && (
                      <button className="small danger" onClick={() => upd(c.appeal_id, { status: 'closed' }, '已关闭（推送 case.closed）')}>
                        关闭 Case
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="panel">
          <div className="panel-title">
            <h2>已发送的 Lark 卡片（开发投递器）</h2>
            <span className="hint">同一任务的后续状态原地更新同一条消息；open_id 以 fail_ 开头的成员投递必然失败</span>
          </div>
          <div className="row wrap" style={{ alignItems: 'flex-start' }}>
            {lark.length === 0 && <div className="hint">暂无</div>}
            {lark.map((m) => (
              <LarkRaw key={m.message_id} m={m} />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** Renders the actual Lark interactive-card JSON we sent. */
function LarkRaw({ m }: { m: any }) {
  const c = m.card;
  const grey = c.header?.template === 'grey';
  return (
    <div className="col gap-s">
      <div className="hint mono">
        {m.message_id} → {m.open_id} · 更新 {m.updates} 次
      </div>
      <div className="lark">
        <div className={`hd ${grey ? 'grey' : ''}`}>{c.header?.title?.content}</div>
        <div className="bd">
          {(c.elements || []).map((el: any, i: number) => {
            if (el.tag === 'div' && el.fields)
              return (
                <div key={i} className="kv">
                  {el.fields.map((f: any, j: number) => {
                    const [k, v] = String(f.text.content).replace(/\*\*/g, '').split('\n');
                    return (
                      <div key={j}>
                        <div className="k">{k}</div>
                        <div>{v}</div>
                      </div>
                    );
                  })}
                </div>
              );
            if (el.tag === 'div' && el.text)
              return (
                <div key={i} className="note" style={{ whiteSpace: 'pre-line' }}>
                  {el.text.content}
                </div>
              );
            if (el.tag === 'note')
              return (
                <div key={i} className="note">
                  {el.elements.map((x: any) => x.content).join(' ')}
                </div>
              );
            if (el.tag === 'action')
              return (
                <div key={i} className="acts">
                  {el.actions.map((a: any, j: number) =>
                    a.url ? (
                      <a key={j} className="link" href={a.url}>
                        {a.text.content}
                      </a>
                    ) : (
                      <span key={j} className={a.type === 'primary' ? 'primary' : ''}>
                        {a.text.content}
                      </span>
                    ),
                  )}
                </div>
              );
            return null;
          })}
        </div>
      </div>
    </div>
  );
}
