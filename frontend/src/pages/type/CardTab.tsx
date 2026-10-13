import { useEffect, useMemo, useState } from 'react';
import { get, post, put } from '../../api';
import { useSession } from '../../App';
import { CardView, LarkPreview, RField, RoundBadge, useRun } from '../../components/ui';

interface CardField {
  field: string;
  label: string;
  format: string;
  lark: boolean;
  list: boolean;
  missing?: string;
}
interface Template {
  name: string;
  title: string;
  fields: CardField[];
  explain: boolean;
  ai: { enabled: boolean; prompt_key: string; version: string };
  buttons: Record<string, { label: string; style: string; confirm: boolean; note_required: boolean }>;
  links: { label: string; url: string }[];
  result_text: string;
  waiting_text: string;
  refreshed_text?: string;
  lark_max_fields: number;
}

const FORMATS: [string, string][] = [
  ['text', '文本'],
  ['mono', '等宽文本'],
  ['mask', '部分掩码'],
  ['money', '金额'],
  ['datetime', '时间'],
];

function render(tpl: string, vars: Record<string, any>) {
  return (tpl || '').replace(/\{\{\s*([a-zA-Z_][\w.]*)\s*\}\}/g, (_, k) => (vars[k] ?? '') + '');
}

function fmt(format: string, v: any): string {
  if (v === undefined || v === null || v === '') return '';
  if (format === 'mask') {
    const s = String(v);
    return s.length > 6 ? `${s.slice(0, 4)}…${s.slice(-4)}` : s;
  }
  if (format === 'money') {
    const n = Number(v);
    return isNaN(n) ? String(v) : n.toLocaleString('en-US', { maximumFractionDigits: 2 });
  }
  if (format === 'datetime') return String(v).replace('T', ' ').slice(0, 16);
  return String(v);
}

export default function CardTab({ typeKey, onPublish, reload }: { typeKey: string; onPublish: () => void; reload: number }) {
  const { me } = useSession();
  const [draft, setDraft] = useState<any>(null);
  const [cards, setCards] = useState<Record<string, Template>>({});
  const [sel, setSel] = useState('');
  const [produced, setProduced] = useState<{ field: string; sensitivity: string }[]>([]);
  const [caps, setCaps] = useState<any[]>([]);
  const [sample, setSample] = useState<Record<string, any>>({});
  const [sampleName, setSampleName] = useState('');
  const [previewNode, setPreviewNode] = useState('');
  const [dirty, setDirty] = useState(false);
  const run = useRun();
  const editable = me?.user.role === 'config' || me?.user.role === 'admin';

  useEffect(() => {
    get(`/api/task-types/${typeKey}/flow-draft`).then((r) => {
      setDraft(r.definition);
      const cs = r.definition.cards || {};
      setCards(cs);
      setSel((s) => s || Object.keys(cs)[0] || '');
      setDirty(false);
    });
    get(`/api/task-types/${typeKey}/config-draft`).then((c) => setProduced(c.spec.fields || []));
    get('/api/capabilities').then(setCaps);
    post(`/api/task-types/${typeKey}/dry-run`, {}).then((r) => {
      const s = r.dry_run.sample?.find((x: any) => x.eligible) || r.dry_run.sample?.[0];
      if (s) {
        setSample(s.fields);
        setSampleName(s.dedupe_key);
      }
    }).catch(() => {});
  }, [typeKey, reload]);

  const tpl = cards[sel];
  const refNodes = useMemo(() => (draft?.graph.nodes || []).filter((n: any) => n.type === 'human' && n.config?.card === sel), [draft, sel]);
  useEffect(() => {
    if (refNodes.length && !refNodes.find((n: any) => n.key === previewNode)) setPreviewNode(refNodes[refNodes.length - 1].key);
  }, [refNodes, previewNode]);
  const buttonKeys = useMemo(() => {
    const out: { key: string; label: string; kind: string; exit?: string }[] = [];
    refNodes.forEach((n: any) => (n.config.buttons || []).forEach((b: any) => !out.find((x) => x.key === b.key) && out.push(b)));
    return out;
  }, [refNodes]);
  const fieldOptions = useMemo(() => {
    const s = new Set(produced.map((p) => p.field));
    caps.forEach((c) => c.outputs.forEach((o: string) => s.add(o)));
    return [...s].sort();
  }, [produced, caps]);

  if (!draft) return <div className="hint">加载中…</div>;

  const patch = (p: Partial<Template>) => {
    setCards({ ...cards, [sel]: { ...tpl, ...p } });
    setDirty(true);
  };
  const setField = (i: number, p: Partial<CardField>) => patch({ fields: tpl.fields.map((f, j) => (j === i ? { ...f, ...p } : f)) });
  const move = (i: number, d: number) => {
    const f = [...tpl.fields];
    const j = i + d;
    if (j < 0 || j >= f.length) return;
    [f[i], f[j]] = [f[j], f[i]];
    patch({ fields: f });
  };
  const save = async () => {
    const r = await run(() => put(`/api/task-types/${typeKey}/flow-draft`, { graph: draft.graph, layout: draft.layout, cards, note: draft.note }), '卡片已保存到流程草稿');
    if (r) {
      setDraft(r.definition);
      setDirty(false);
    }
  };
  const addTemplate = () => {
    const k = window.prompt('模板 key（字母、下划线）');
    if (!k || cards[k]) return;
    setCards({ ...cards, [k]: { name: k, title: '', fields: [], explain: true, ai: { enabled: false, prompt_key: '', version: '' }, buttons: {}, links: [], result_text: '', waiting_text: '', lark_max_fields: 4 } });
    setSel(k);
    setDirty(true);
  };

  // preview
  const node = refNodes.find((n: any) => n.key === previewNode);
  const vars: Record<string, any> = { ...sample, 'node.round_label': node?.config.round_label || '' };
  const sensitive = new Set(produced.filter((p) => p.sensitivity === 'sensitive').map((p) => p.field));
  const rfields: RField[] = (tpl?.fields || []).map((f) => {
    const v = fmt(f.format, vars[f.field]);
    return { field: f.field, label: f.label, value: v || f.missing || '—', format: f.format, lark: f.lark, list: f.list, sensitive: sensitive.has(f.field), missing: !v };
  });
  const view: CardView | null = tpl
    ? {
        task_id: 0,
        card_rev: 1,
        work_item_id: 0,
        state: 'actionable',
        web_url: '',
        card: {
          title: render(node?.config.title || tpl.title, vars),
          round_label: node?.config.round_label || '',
          round: 1,
          fields: rfields,
          buttons: (node?.config.buttons || []).map((b: any) => ({ key: b.key, label: tpl.buttons?.[b.key]?.label || b.label, style: tpl.buttons?.[b.key]?.style || 'default', kind: b.kind, confirm: false, note_required: false })),
          links: tpl.links,
          lark_max: tpl.lark_max_fields || 4,
          ai: tpl.ai,
          explain: tpl.explain,
        },
      }
    : null;

  return (
    <div className="col" style={{ maxWidth: 1260, margin: '0 auto' }}>
      <div className="row between">
        <div className="row">
          <b>卡片随流程版本化 · 流程草稿 r{draft.revision} · 基于 v{draft.base_version || '—'}</b>
          {dirty && <span className="pill amber">未保存</span>}
          <span className="hint">被 {refNodes.length} 个人工节点引用{refNodes.length ? '：' + refNodes.map((n: any) => n.name).join('、') : ''}</span>
        </div>
        <div className="row gap-s">
          <button disabled={!editable} onClick={save}>
            保存
          </button>
          <button className="primary" disabled={dirty} onClick={onPublish}>
            发布…
          </button>
        </div>
      </div>
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <div className="col grow">
          <div className="panel section-gap">
            <div className="row">
              <label className="field">
                <span>模板</span>
                <select value={sel} onChange={(e) => setSel(e.target.value)}>
                  {Object.keys(cards).map((k) => (
                    <option key={k} value={k}>
                      {cards[k].name || k}
                    </option>
                  ))}
                </select>
              </label>
              <button className="small" style={{ alignSelf: 'flex-end' }} onClick={addTemplate}>
                + 模板
              </button>
            </div>
            {tpl && (
              <>
                <label className="field">
                  <span>名称</span>
                  <input value={tpl.name} onChange={(e) => patch({ name: e.target.value })} />
                </label>
                <label className="field">
                  <span>标题模板（可引用字段和节点变量，如 {'{{node.round_label}}'}）</span>
                  <input value={tpl.title} onChange={(e) => patch({ title: e.target.value })} />
                </label>
                <div className="hint">
                  节点覆盖：{refNodes.map((n: any) => `${n.name} → 「${n.config.title || '模板标题'}」`).join(' · ') || '—'}
                </div>
              </>
            )}
          </div>
          {tpl && (
            <>
              <div className="panel">
                <div className="panel-title">
                  <h2>字段</h2>
                  <span className="hint">只能选快照字段和取数节点的输出 · 调整顺序</span>
                </div>
                <datalist id="card-fields">
                  {fieldOptions.map((f) => (
                    <option key={f} value={f} />
                  ))}
                </datalist>
                <table className="tbl">
                  <thead>
                    <tr>
                      <th></th>
                      <th>字段</th>
                      <th>标签</th>
                      <th>格式</th>
                      <th>Lark 显示</th>
                      <th>列表行显示</th>
                      <th>缺失时</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {tpl.fields.map((f, i) => (
                      <tr key={i}>
                        <td style={{ whiteSpace: 'nowrap' }}>
                          <button className="ghost small" onClick={() => move(i, -1)}>
                            ↑
                          </button>
                          <button className="ghost small" onClick={() => move(i, 1)}>
                            ↓
                          </button>
                        </td>
                        <td>
                          <input list="card-fields" className="mono" value={f.field} onChange={(e) => setField(i, { field: e.target.value })} style={{ borderColor: f.field && !fieldOptions.includes(f.field) ? 'var(--red)' : undefined }} />
                        </td>
                        <td>
                          <input value={f.label} onChange={(e) => setField(i, { label: e.target.value })} />
                        </td>
                        <td>
                          <select value={f.format} onChange={(e) => setField(i, { format: e.target.value })}>
                            {FORMATS.map(([k, l]) => (
                              <option key={k} value={k}>
                                {l}
                                {sensitive.has(f.field) ? ' · 敏感' : ''}
                              </option>
                            ))}
                          </select>
                        </td>
                        <td>
                          <input type="checkbox" checked={f.lark} onChange={(e) => setField(i, { lark: e.target.checked })} />
                        </td>
                        <td>
                          <input type="checkbox" checked={f.list} onChange={(e) => setField(i, { list: e.target.checked })} />
                        </td>
                        <td>
                          <input value={f.missing || ''} placeholder="—" onChange={(e) => setField(i, { missing: e.target.value })} />
                        </td>
                        <td>
                          <button className="ghost" onClick={() => patch({ fields: tpl.fields.filter((_, j) => j !== i) })}>
                            ✕
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <button className="small mt" onClick={() => patch({ fields: [...tpl.fields, { field: '', label: '', format: 'text', lark: true, list: false }] })}>
                  + 字段
                </button>
                <div className="hint mt">敏感字段：RM 和 Leader 看完整值，其他角色看掩码</div>
              </div>

              <div className="panel section-gap">
                <div className="panel-title">
                  <h2>说明区与 AI 摘要</h2>
                </div>
                <label className="check">
                  <input type="checkbox" checked={tpl.explain} onChange={(e) => patch({ explain: e.target.checked })} />
                  显示「为什么 / 为什么是我 / 接下来」（系统根据运行数据生成）
                </label>
                <label className="check">
                  <input type="checkbox" checked={tpl.ai?.enabled} onChange={(e) => patch({ ai: { ...tpl.ai, enabled: e.target.checked } })} />
                  显示 AI 摘要
                </label>
                <div className="row">
                  <label className="field">
                    <span>Prompt Key</span>
                    <input value={tpl.ai?.prompt_key || ''} onChange={(e) => patch({ ai: { ...tpl.ai, prompt_key: e.target.value } })} />
                  </label>
                  <label className="field">
                    <span>版本（固定）</span>
                    <input value={tpl.ai?.version || ''} onChange={(e) => patch({ ai: { ...tpl.ai, version: e.target.value } })} />
                  </label>
                </div>
                <div className="hint">摘要失败时只隐藏摘要，字段照常展示</div>
              </div>

              <div className="panel section-gap">
                <div className="panel-title">
                  <h2>按钮与链接</h2>
                  <span className="hint">按钮去向在处理流程里定义，这里只管展示</span>
                </div>
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>按钮</th>
                      <th>出口</th>
                      <th>按钮文字</th>
                      <th>样式</th>
                      <th>二次确认</th>
                      <th>必填备注</th>
                    </tr>
                  </thead>
                  <tbody>
                    {buttonKeys.map((b) => {
                      const st = tpl.buttons?.[b.key] || { label: '', style: 'default', confirm: false, note_required: false };
                      const set = (p: any) => patch({ buttons: { ...tpl.buttons, [b.key]: { ...st, ...p } } });
                      return (
                        <tr key={b.key}>
                          <td className="mono">{b.key}</td>
                          <td className="mono hint">{b.kind === 'delay' ? '延迟' : b.exit}</td>
                          <td>
                            <input value={st.label} placeholder={b.label} onChange={(e) => set({ label: e.target.value })} />
                          </td>
                          <td>
                            <select value={st.style} onChange={(e) => set({ style: e.target.value })}>
                              <option value="primary">主按钮</option>
                              <option value="default">次按钮</option>
                              <option value="danger">危险</option>
                            </select>
                          </td>
                          <td>
                            <input type="checkbox" checked={st.confirm} onChange={(e) => set({ confirm: e.target.checked })} />
                          </td>
                          <td>
                            <input type="checkbox" checked={st.note_required} onChange={(e) => set({ note_required: e.target.checked })} />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
                <div className="field">
                  <span className="hint">链接（域名白名单，服务端签发）</span>
                  {tpl.links.map((l, i) => (
                    <div className="row gap-s" key={i}>
                      <input value={l.label} style={{ width: 160 }} onChange={(e) => patch({ links: tpl.links.map((x, j) => (j === i ? { ...x, label: e.target.value } : x)) })} />
                      <input className="grow mono" value={l.url} onChange={(e) => patch({ links: tpl.links.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)) })} />
                      <button className="ghost" onClick={() => patch({ links: tpl.links.filter((_, j) => j !== i) })}>
                        ✕
                      </button>
                    </div>
                  ))}
                  <div>
                    <button className="small" onClick={() => patch({ links: [...tpl.links, { label: '', url: 'https://' }] })}>
                      + 链接
                    </button>
                  </div>
                </div>
                <div className="row wrap">
                  <label className="field grow">
                    <span>结果态（可用 {'{{action.label}} {{actor}} {{time}}'}）</span>
                    <input value={tpl.result_text} onChange={(e) => patch({ result_text: e.target.value })} />
                  </label>
                  <label className="field grow">
                    <span>等待态（可用 {'{{next.time}}'}）</span>
                    <input value={tpl.waiting_text} onChange={(e) => patch({ waiting_text: e.target.value })} />
                  </label>
                  <label className="field">
                    <span>Lark 最多字段</span>
                    <input type="number" style={{ width: 90 }} value={tpl.lark_max_fields} onChange={(e) => patch({ lark_max_fields: Number(e.target.value) })} />
                  </label>
                </div>
              </div>
            </>
          )}
        </div>

        <div className="col" style={{ width: 380, flexShrink: 0, position: 'sticky', top: 0 }}>
          <div className="panel section-gap">
            <div className="panel-title">
              <h2>预览</h2>
              <span className="hint">样本 {sampleName || '（无试跑样本）'}</span>
            </div>
            {refNodes.length > 1 && (
              <div className="seg">
                {refNodes.map((n: any) => (
                  <button key={n.key} className={previewNode === n.key ? 'on' : ''} onClick={() => setPreviewNode(n.key)}>
                    {n.name}
                  </button>
                ))}
              </div>
            )}
            {view && (
              <>
                <div className="hint">Lark 卡片</div>
                <LarkPreview view={view} />
                <div className="hint">待办列表行</div>
                <div className="task-item">
                  <div className="row gap-s">
                    <RoundBadge label={view.card.round_label || '新建'} />
                    <span className="title">{view.card.title}</span>
                  </div>
                  <div className="sub">
                    {rfields
                      .filter((f) => f.list)
                      .slice(0, 3)
                      .map((f) => f.value)
                      .join(' · ')}
                  </div>
                </div>
                <div className="hint">Web 详情</div>
                <div className="ai" style={{ borderStyle: 'solid' }}>
                  全部 {rfields.length} 个字段{tpl.explain ? ' · 说明区' : ''}
                  {tpl.ai?.enabled ? ' · AI 摘要' : ''} · 时间线{tpl.links.length ? ' · ' + tpl.links.map((l) => l.label).join('、') : ''}
                </div>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
