import { useCallback, useEffect, useMemo, useState } from 'react';
import { fmtTime, get, post, put } from '../../api';
import { useSession } from '../../App';
import { ConditionEditor, Group, useRun } from '../../components/ui';
import type { TypeInfo } from '../TaskTypeDetail';

interface FieldMap {
  column: string;
  field: string;
  type: string;
  required: boolean;
  sensitivity: string;
}

interface Spec {
  trigger: { sources: string[]; datasource?: string; query?: string; push_event?: string; flow_key?: string; mcp_tool?: string };
  fields: FieldMap[];
  eligibility: Group;
  live_refresh: { capability: string; args: Record<string, string> }[];
  invalidation: { events: string[]; recheck_every: string };
  schedule: { cron: string; timezone: string; enabled: boolean };
  max_per_run: number;
}

const SOURCES: [string, string][] = [
  ['sql', '数仓 SQL'],
  ['mcp', 'MCP'],
  ['push', '业务 Push'],
  ['flow', '自动化流程（巡检）'],
];
const PRESETS: [string, string][] = [
  ['*/5 * * * *', '每 5 分钟'],
  ['0 * * * *', '每小时'],
  ['0 9 * * *', '每天 09:00'],
];

export default function ConfigTab({ typeKey, info, onPublish, onChanged, reload }: { typeKey: string; info: TypeInfo; onPublish: () => void; onChanged: () => void; reload: number }) {
  const { me } = useSession();
  const [draft, setDraft] = useState<any>(null);
  const [spec, setSpec] = useState<Spec | null>(null);
  const [dirty, setDirty] = useState(false);
  const [sqlCheck, setSqlCheck] = useState<any>(null);
  const [dry, setDry] = useState<any>(null);
  const [flowNeeds, setFlowNeeds] = useState<string[]>([]);
  const [datasources, setDatasources] = useState<string[]>([]);
  const [caps, setCaps] = useState<any[]>([]);
  const run = useRun();
  const editable = me?.user.role === 'config' || me?.user.role === 'admin';

  const load = useCallback(() => {
    get(`/api/task-types/${typeKey}/config-draft`).then((d) => {
      setDraft(d);
      setSpec(normalize(d.spec));
      setDirty(false);
    });
    get(`/api/task-types/${typeKey}/flow-draft`).then((r) => setFlowNeeds(r.validation.inputs || []));
  }, [typeKey]);
  useEffect(load, [load, reload]);
  useEffect(() => {
    get('/api/datasources').then(setDatasources);
    get('/api/capabilities').then(setCaps);
  }, []);

  // live SQL guard feedback
  useEffect(() => {
    if (!spec?.trigger.query) return;
    const h = setTimeout(() => post('/api/sql/check', { query: spec.trigger.query }).then(setSqlCheck), 300);
    return () => clearTimeout(h);
  }, [spec?.trigger.query]);

  const produced = useMemo(() => (spec ? spec.fields.map((f) => f.field).filter(Boolean) : []), [spec]);
  if (!spec || !draft) return <div className="hint">加载中…</div>;
  const t = info.type;
  const sched = info.schedule?.schedule;
  const update = (patch: Partial<Spec>) => {
    setSpec({ ...spec, ...patch });
    setDirty(true);
  };
  const setField = (i: number, patch: Partial<FieldMap>) => update({ fields: spec.fields.map((f, j) => (j === i ? { ...f, ...patch } : f)) });
  const missing = flowNeeds.filter((f) => !produced.includes(f));
  const dedupe = [...info.dedupe_fields, ...(t.round_field ? [t.round_field] : [])];

  const save = async () => {
    const r = await run(() => put(`/api/task-types/${typeKey}/config-draft`, { spec, note: draft.note }), '草稿已保存');
    if (r) {
      setDraft(r);
      setDirty(false);
    }
  };
  const dryRun = async () => {
    const r = await run(() => post(`/api/task-types/${typeKey}/dry-run`, { spec }));
    if (r) setDry(r);
  };

  return (
    <div className="col" style={{ maxWidth: 1200, margin: '0 auto' }}>
      <div className="row between">
        <div className="row">
          <h2>
            配置草稿 r{draft.revision} · 基于 v{draft.base_version || '—'}
          </h2>
          {dirty && <span className="pill amber">未保存</span>}
          {draft.note && <span className="hint">改动：{draft.note}</span>}
        </div>
        <div className="row gap-s">
          <button disabled={!editable} onClick={save}>
            保存草稿
          </button>
          <button onClick={dryRun}>试跑</button>
          <button className="primary" disabled={dirty} title={dirty ? '请先保存草稿' : ''} onClick={onPublish}>
            发布…
          </button>
        </div>
      </div>

      <div className="row" style={{ alignItems: 'flex-start' }}>
        <div className="col grow">
          <div className="panel">
            <div className="kvline hint">
              <span>对象 {t.object_type}</span>·<span>去重 <code>{dedupe.join(' + ')}</code></span>·<span>负责团队 {t.team}</span>
            </div>
            <div className="hint">任务类型创建后不可改；要改去重方式需新建任务类型</div>
          </div>

          <div className="panel section-gap">
            <div className="panel-title">
              <h2>触发来源</h2>
            </div>
            <div className="row wrap">
              {SOURCES.map(([k, l]) => (
                <button key={k} className={`chip ${spec.trigger.sources.includes(k) ? 'on' : ''}`} onClick={() =>
                  update({ trigger: { ...spec.trigger, sources: spec.trigger.sources.includes(k) ? spec.trigger.sources.filter((s) => s !== k) : [...spec.trigger.sources, k] } })
                }>
                  {l}
                </button>
              ))}
            </div>
            {spec.trigger.sources.includes('sql') && (
              <>
                <div className="row">
                  <label className="field">
                    <span>数据源（只读）</span>
                    <select value={spec.trigger.datasource} onChange={(e) => update({ trigger: { ...spec.trigger, datasource: e.target.value } })}>
                      {datasources.map((d) => (
                        <option key={d}>{d}</option>
                      ))}
                    </select>
                  </label>
                  <span className="hint" style={{ alignSelf: 'flex-end' }}>
                    可用变量：<code>{'{{scan_from}}'}</code> 启用/回扫起点 · <code>{'{{last_scan_at}}'}</code> 上次成功扫描 · <code>{'{{now}}'}</code>
                  </span>
                </div>
                <label className="field">
                  <span>查询 · SQL 参数只能出现在值的位置，执行时绑定</span>
                  <textarea rows={7} value={spec.trigger.query || ''} onChange={(e) => update({ trigger: { ...spec.trigger, query: e.target.value } })} />
                </label>
                {sqlCheck && (
                  <div className={sqlCheck.ok ? 'hint' : 'issue error'}>
                    {sqlCheck.ok ? `✓ 只读检查通过 · ${sqlCheck.params} 个参数绑定` : `✕ ${sqlCheck.error}`}
                  </div>
                )}
              </>
            )}
            {spec.trigger.sources.includes('push') && (
              <div className="hint">
                业务 Push：<code>POST /api/push/{typeKey}</code>，payload 键名对应下方"查询列"，<code>event_id</code> 去重。
              </div>
            )}
            {spec.trigger.sources.includes('mcp') && (
              <label className="field">
                <span>MCP 工具</span>
                <input value={spec.trigger.mcp_tool || ''} onChange={(e) => update({ trigger: { ...spec.trigger, mcp_tool: e.target.value } })} />
              </label>
            )}
            {spec.trigger.sources.includes('flow') && <div className="hint">已发布的自动化流程（巡检）负责发现，结果通过业务 Push 接口交给 Bywork 建任务。</div>}
          </div>

          <div className="panel">
            <div className="panel-title">
              <h2>字段映射</h2>
              <span className="hint">这张表就是本版本的产出字段，写入任务快照</span>
            </div>
            <table className="tbl">
              <thead>
                <tr>
                  <th>查询列</th>
                  <th>任务字段</th>
                  <th>类型</th>
                  <th>必填</th>
                  <th>敏感级别</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {spec.fields.map((f, i) => (
                  <tr key={i}>
                    <td>
                      <input className="mono" value={f.column} onChange={(e) => setField(i, { column: e.target.value })} />
                    </td>
                    <td>
                      <input className="mono" value={f.field} onChange={(e) => setField(i, { field: e.target.value })} />
                    </td>
                    <td>
                      <select value={f.type} onChange={(e) => setField(i, { type: e.target.value })}>
                        {['string', 'number', 'bool', 'datetime'].map((x) => (
                          <option key={x}>{x}</option>
                        ))}
                      </select>
                    </td>
                    <td>
                      <input type="checkbox" checked={f.required} onChange={(e) => setField(i, { required: e.target.checked })} />
                    </td>
                    <td>
                      <select value={f.sensitivity} onChange={(e) => setField(i, { sensitivity: e.target.value })}>
                        <option value="normal">普通</option>
                        <option value="sensitive">敏感</option>
                      </select>
                    </td>
                    <td>
                      <button className="ghost" onClick={() => update({ fields: spec.fields.filter((_, j) => j !== i) })}>
                        ✕
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <button className="small mt" onClick={() => update({ fields: [...spec.fields, { column: '', field: '', type: 'string', required: false, sensitivity: 'normal' }] })}>
              + 字段
            </button>
          </div>

          <div className="panel">
            <div className="panel-title">
              <h2>资格条件</h2>
              <span className="hint">全部满足才建任务；延迟唤醒前也会重新校验</span>
            </div>
            <ConditionEditor value={spec.eligibility} fields={produced} onChange={(g) => update({ eligibility: g })} />
          </div>

          <div className="panel section-gap">
            <div className="panel-title">
              <h2>失效与复查</h2>
              <span className="hint">业务对象在别处关闭时自动以「Case 已失效」关闭任务</span>
            </div>
            <div className="row wrap">
              <label className="field">
                <span>失效事件（逗号分隔）</span>
                <input value={spec.invalidation.events.join(', ')} onChange={(e) => update({ invalidation: { ...spec.invalidation, events: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) } })} />
              </label>
              <label className="field">
                <span>无事件时复查间隔</span>
                <input value={spec.invalidation.recheck_every} placeholder="如 6h，留空不复查" onChange={(e) => update({ invalidation: { ...spec.invalidation, recheck_every: e.target.value } })} />
              </label>
            </div>
            <div className="field">
              <span className="hint">复查前的实时读取（data.*）</span>
              {spec.live_refresh.map((r, i) => (
                <div className="row gap-s" key={i}>
                  <select value={r.capability} onChange={(e) => update({ live_refresh: spec.live_refresh.map((x, j) => (j === i ? { ...x, capability: e.target.value } : x)) })}>
                    {caps.map((c) => (
                      <option key={c.name} value={c.name}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                  <input className="grow mono" value={JSON.stringify(r.args)} onChange={(e) => {
                    try {
                      const args = JSON.parse(e.target.value);
                      update({ live_refresh: spec.live_refresh.map((x, j) => (j === i ? { ...x, args } : x)) });
                    } catch {
                      /* keep typing */
                    }
                  }} />
                  <button className="ghost" onClick={() => update({ live_refresh: spec.live_refresh.filter((_, j) => j !== i) })}>
                    ✕
                  </button>
                </div>
              ))}
              <div>
                <button className="small" onClick={() => update({ live_refresh: [...spec.live_refresh, { capability: caps[0]?.name || '', args: {} }] })}>
                  + 实时读取
                </button>
              </div>
            </div>
          </div>

          <div className="panel section-gap">
            <div className="panel-title">
              <h2>调度</h2>
              <span className="hint">扫描 cron 在 Bywork 内配置；XXL-Job 只有三个固定心跳</span>
            </div>
            <div className="row wrap">
              {PRESETS.map(([c, l]) => (
                <button key={c} className={`chip ${spec.schedule.cron === c ? 'on' : ''}`} onClick={() => update({ schedule: { ...spec.schedule, cron: c } })}>
                  {l}
                </button>
              ))}
              <label className="field">
                <span>cron（5 段，不支持秒）</span>
                <input className="mono" value={spec.schedule.cron} onChange={(e) => update({ schedule: { ...spec.schedule, cron: e.target.value } })} />
              </label>
              <label className="field">
                <span>时区</span>
                <select value={spec.schedule.timezone} onChange={(e) => update({ schedule: { ...spec.schedule, timezone: e.target.value } })}>
                  {['UTC', 'Asia/Kuala_Lumpur', 'Asia/Singapore', 'Asia/Dubai', 'Asia/Shanghai'].map((z) => (
                    <option key={z}>{z}</option>
                  ))}
                </select>
              </label>
              <label className="field">
                <span>单次建任务上限</span>
                <input type="number" value={spec.max_per_run} onChange={(e) => update({ max_per_run: Number(e.target.value) })} style={{ width: 110 }} />
              </label>
              <label className="check" style={{ alignSelf: 'flex-end' }}>
                <input type="checkbox" checked={spec.schedule.enabled} onChange={(e) => update({ schedule: { ...spec.schedule, enabled: e.target.checked } })} />
                启用定时扫描
              </label>
            </div>
            {sched && (
              <>
                <div className="hint">
                  当前计划（v{sched.config_version}）：<code>{sched.cron}</code> {sched.timezone} · 状态 <b>{sched.status}</b>
                  {sched.pause_reason && <span className="issue error"> · {sched.pause_reason}</span>} · 接下来：
                  {(info.schedule.next_fires || []).map((n: number) => fmtTime(n, false)).join('、')} · 起点 {fmtTime(sched.watermark)} · 超过上限时暂停本任务类型并告警
                </div>
                <div className="row gap-s">
                  <button className="small" disabled={!editable} onClick={async () => {
                    const r = await run(() => post(`/api/task-types/${typeKey}/run-now`));
                    if (r) {
                      onChanged();
                    }
                  }}>
                    立即执行一次
                  </button>
                  {sched.status === 'active' ? (
                    <button className="small" onClick={async () => (await run(() => post(`/api/task-types/${typeKey}/status`, { action: 'pause_schedule' }), '已暂停')) && onChanged()}>
                      暂停
                    </button>
                  ) : (
                    <button className="small" onClick={async () => (await run(() => post(`/api/task-types/${typeKey}/status`, { action: 'resume_schedule' }), '已恢复')) && onChanged()}>
                      恢复扫描
                    </button>
                  )}
                </div>
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>最近执行</th>
                      <th>耗时</th>
                      <th>扫描行数</th>
                      <th>新建任务</th>
                      <th>结果</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(info.schedule.runs || []).slice(0, 6).map((r: any) => (
                      <tr key={r.id}>
                        <td>{fmtTime(r.fired_at)}</td>
                        <td>{r.duration_ms}ms</td>
                        <td>{r.rows_scanned}</td>
                        <td>{r.tasks_created}</td>
                        <td>
                          <span className={`pill ${r.result === 'success' ? 'green' : r.result === 'skipped' ? '' : 'red'}`}>{r.result}</span> <span className="hint">{r.message}</span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}
          </div>
        </div>

        <div className="col" style={{ width: 360, flexShrink: 0 }}>
          <div className="panel">
            <div className="panel-title">
              <h2>字段契约</h2>
            </div>
            <div>
              处理流程草稿需要 {flowNeeds.length} 个快照字段，本草稿{missing.length ? <b style={{ color: 'var(--red)' }}>缺少 {missing.join('、')}</b> : '全部产出'}
            </div>
            <div className="kvline mt">
              {flowNeeds.map((f) => (
                <span key={f} className={`pill ${produced.includes(f) ? 'green' : 'red'}`}>
                  {f}
                </span>
              ))}
            </div>
            <div className="hint mt">两边都有改动时请「一起发布」，避免出现不一致的窗口。等待之后的判断由流程实时读取，不依赖快照。</div>
          </div>
          {dry && <DryRunPanel dry={dry} />}
          <div className="panel">
            <div className="panel-title">
              <h2>任务类型状态</h2>
            </div>
            <div className="row gap-s wrap hint">
              <span className={t.status === 'draft' ? 'pill green' : 'pill'}>草稿</span>→
              <span className={t.status === 'shadow' ? 'pill violet' : 'pill'}>影子运行（可选）</span>→
              <span className={t.status === 'enabled' ? 'pill green' : 'pill'}>启用</span>⇄
              <span className={t.status === 'disabled' ? 'pill red' : 'pill'}>停用</span>
            </div>
            <p className="hint">
              影子运行由创建者启动和结束：任务照常创建、流程照常执行，但不推送、不进入任何人的待办。用来确认几天后才发生的行为，比如 7 天复查和跨扫描去重。
            </p>
            <p className="hint">停用后已有任务保留，不再新建和推送；在途定时到期只记录不推送。</p>
          </div>
        </div>
      </div>
    </div>
  );
}

function DryRunPanel({ dry }: { dry: any }) {
  const d = dry.dry_run;
  return (
    <div className="panel">
      <div className="panel-title">
        <h2>试跑结果</h2>
        <span className={`pill ${d.ok && !(dry.errors || []).length ? 'green' : 'red'}`}>{d.ok && !(dry.errors || []).length ? '通过' : '未通过'}</span>
      </div>
      <ul className="checks">
        {[...(dry.errors || []), ...(d.errors || [])].map((e: string, i: number) => (
          <li key={'e' + i} className="error">
            <span className="ic">✕</span>
            {e}
          </li>
        ))}
        {(d.checks || []).map((c: string, i: number) => (
          <li key={i} className="pass">
            <span className="ic">✓</span>
            {c}
          </li>
        ))}
      </ul>
      <div className="mt">
        扫描 {d.rows} 行 · 满足资格 {d.eligible} · 预计新建 <b>{d.new_tasks}</b> 个任务 · 通知 <b>{d.notify_rms}</b> 位 RM
      </div>
      {d.sample?.length > 0 && (
        <details className="mt">
          <summary className="hint">样本（{d.sample.length}）</summary>
          <table className="tbl">
            <tbody>
              {d.sample.map((s: any, i: number) => (
                <tr key={i}>
                  <td className="mono">{s.dedupe_key || '—'}</td>
                  <td>
                    {s.eligible ? <span className="pill green">{s.exists ? '已存在' : '新建'}</span> : <span className="pill">不满足</span>}
                    {s.error && <div className="hint">{s.error}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  );
}

function normalize(s: any): Spec {
  return {
    trigger: { sources: [], ...(s?.trigger || {}) },
    fields: s?.fields || [],
    eligibility: s?.eligibility || { all: [] },
    live_refresh: s?.live_refresh || [],
    invalidation: { events: [], recheck_every: '', ...(s?.invalidation || {}) },
    schedule: { cron: '*/5 * * * *', timezone: 'UTC', enabled: true, ...(s?.schedule || {}) },
    max_per_run: s?.max_per_run ?? 200,
  };
}
