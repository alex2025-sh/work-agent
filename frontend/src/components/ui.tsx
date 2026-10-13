import { createContext, ReactNode, useCallback, useContext, useEffect, useState } from 'react';
import { closeReasonText, typeStatusText } from '../api';

// ---------- toast ----------

type ToastFn = (msg: string, err?: boolean) => void;
const ToastCtx = createContext<ToastFn>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
  const [t, setT] = useState<{ msg: string; err?: boolean } | null>(null);
  const show = useCallback<ToastFn>((msg, err) => setT({ msg, err }), []);
  useEffect(() => {
    if (!t) return;
    const h = setTimeout(() => setT(null), 3200);
    return () => clearTimeout(h);
  }, [t]);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      {t && <div className={`toast ${t.err ? 'err' : ''}`}>{t.msg}</div>}
    </ToastCtx.Provider>
  );
}

export const useToast = () => useContext(ToastCtx);

/** run wraps an async action with error toasts. */
export function useRun() {
  const toast = useToast();
  return useCallback(
    async <T,>(fn: () => Promise<T>, ok?: string): Promise<T | undefined> => {
      try {
        const r = await fn();
        if (ok) toast(ok);
        return r;
      } catch (e: any) {
        toast(e?.message || String(e), true);
        return undefined;
      }
    },
    [toast],
  );
}

// ---------- modal ----------

export function Modal(props: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; width?: number }) {
  return (
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && props.onClose()}>
      <div className="modal" style={props.width ? { width: props.width } : undefined}>
        <div className="mh row between">
          <h2>{props.title}</h2>
          <button className="ghost" onClick={props.onClose}>
            ✕
          </button>
        </div>
        <div className="mb">{props.children}</div>
        {props.footer && <div className="mf">{props.footer}</div>}
      </div>
    </div>
  );
}

// ---------- pills ----------

export function StatusPill({ status, reason }: { status: string; reason?: string }) {
  if (status === 'PROCESSING') return <span className="pill green">待处理</span>;
  if (status === 'OPEN') return <span className="pill amber">等待中</span>;
  return <span className="pill">{closeReasonText[reason || ''] || '已结束'}</span>;
}

export function TypeStatusPill({ status }: { status: string }) {
  const cls = status === 'enabled' ? 'green' : status === 'shadow' ? 'violet' : status === 'disabled' ? 'red' : '';
  return <span className={`pill ${cls}`}>{typeStatusText[status] || status}</span>;
}

export function RoundBadge({ label }: { label?: string }) {
  if (!label) return null;
  const cls = label === '新建' ? 'green' : label.includes('7') || label.startsWith('T+') ? 'amber' : 'blue';
  return <span className={`pill ${cls}`}>{label}</span>;
}

// ---------- facts ----------

export interface RField {
  field: string;
  label: string;
  value: string;
  format: string;
  lark: boolean;
  list: boolean;
  sensitive: boolean;
  missing: boolean;
}

export function Facts({ fields }: { fields: RField[] }) {
  return (
    <div className="facts">
      {fields.map((f) => (
        <div key={f.field}>
          <div className="k">{f.label}</div>
          <div className={`v ${f.missing ? 'missing' : ''} ${f.format === 'mono' ? 'mono' : ''}`}>{f.value}</div>
        </div>
      ))}
    </div>
  );
}

// ---------- Lark card preview ----------

export interface CardView {
  task_id: number;
  card: {
    title: string;
    round_label: string;
    round: number;
    fields: RField[];
    buttons: { key: string; label: string; style: string; kind: string; confirm: boolean; note_required: boolean }[];
    links: { label: string; url: string }[];
    lark_max: number;
    ai: { enabled: boolean; prompt_key: string; version: string };
    explain: boolean;
  };
  card_rev: number;
  work_item_id: number;
  state: string;
  result?: string;
  waiting?: string;
  previous?: string;
  web_url: string;
}

export function LarkPreview({ view, typeName }: { view: CardView; typeName?: string }) {
  const c = view.card;
  const fields = (c.fields || []).filter((f) => f.lark).slice(0, c.lark_max || 4);
  const grey = view.state !== 'actionable';
  return (
    <div className="lark">
      <div className={`hd ${grey ? 'grey' : ''}`}>
        <small>
          Bywork · {typeName || ''}
          {c.round > 1 ? ` · 第 ${c.round} 轮` : ''}
        </small>
        {c.title || '（无标题）'}
      </div>
      <div className="bd">
        <div className="kv">
          {fields.map((f) => (
            <div key={f.field}>
              <div className="k">{f.label}</div>
              <div>{f.value}</div>
            </div>
          ))}
        </div>
        {view.previous && <div className="note">上一轮 · {view.previous}</div>}
        {view.state === 'actionable' ? (
          <div className="acts">
            {c.buttons.map((b) => (
              <span key={b.key} className={b.style === 'primary' ? 'primary' : ''}>
                {b.label}
              </span>
            ))}
          </div>
        ) : (
          (view.result || view.waiting) && (
            <div className="note">
              {view.result && <div>{view.result}</div>}
              {view.waiting && <div>{view.waiting}</div>}
            </div>
          )
        )}
        <div className="link">在 Bywork 中查看详情与追问</div>
      </div>
    </div>
  );
}

// ---------- condition editor ----------

export interface Rule {
  field: string;
  op: string;
  value?: any;
  label?: string;
}
export interface Group {
  all?: Rule[];
  any?: Rule[];
}

export const OPS: [string, string][] = [
  ['eq', '等于'],
  ['ne', '不等于'],
  ['in', '属于'],
  ['not_in', '不属于'],
  ['gt', '>'],
  ['gte', '≥'],
  ['lt', '<'],
  ['lte', '≤'],
  ['exists', '有值'],
  ['not_exists', '无值'],
  ['contains', '包含'],
  ['is_true', '为真'],
  ['is_false', '为假'],
  ['elapsed_gte', '距今 ≥'],
  ['elapsed_lt', '距今 <'],
];
const noValue = new Set(['exists', 'not_exists', 'is_true', 'is_false']);

function valueToText(v: any) {
  if (Array.isArray(v)) return v.join(', ');
  return v === undefined || v === null ? '' : String(v);
}

export function ConditionEditor({ value, onChange, fields }: { value: Group; onChange: (g: Group) => void; fields: string[] }) {
  const rules = value.all || [];
  const set = (i: number, patch: Partial<Rule>) => {
    const next = rules.map((r, j) => (j === i ? { ...r, ...patch } : r));
    onChange({ ...value, all: next });
  };
  const listId = 'fields-' + Math.random().toString(36).slice(2, 8);
  return (
    <div className="col gap-s">
      <datalist id={listId}>
        {fields.map((f) => (
          <option key={f} value={f} />
        ))}
      </datalist>
      {rules.length === 0 && <div className="hint">没有条件</div>}
      {rules.map((r, i) => (
        <div key={i} className="row gap-s">
          <input list={listId} style={{ width: 190 }} value={r.field} placeholder="字段" onChange={(e) => set(i, { field: e.target.value })} />
          <select value={r.op} onChange={(e) => set(i, { op: e.target.value })}>
            {OPS.map(([k, l]) => (
              <option key={k} value={k}>
                {l}
              </option>
            ))}
          </select>
          {!noValue.has(r.op) && (
            <input
              className="grow"
              value={valueToText(r.value)}
              placeholder={r.op.startsWith('elapsed') ? '如 48h / 7d' : r.op.endsWith('in') ? '逗号分隔' : '值'}
              onChange={(e) => {
                const t = e.target.value;
                set(i, { value: r.op === 'in' || r.op === 'not_in' ? t.split(',').map((s) => s.trim()).filter(Boolean) : t });
              }}
            />
          )}
          <input style={{ width: 150 }} value={r.label || ''} placeholder="说明（显示在任务里）" onChange={(e) => set(i, { label: e.target.value })} />
          <button className="ghost" onClick={() => onChange({ ...value, all: rules.filter((_, j) => j !== i) })}>
            ✕
          </button>
        </div>
      ))}
      <div>
        <button className="small" onClick={() => onChange({ ...value, all: [...rules, { field: '', op: 'eq', value: '' }] })}>
          + 条件
        </button>
      </div>
    </div>
  );
}

// ---------- JSON-schema form (node property panel) ----------

export interface Widgets {
  conditions: (v: any, set: (v: any) => void) => ReactNode;
  capability: (v: any, set: (v: any) => void) => ReactNode;
  card: (v: any, set: (v: any) => void) => ReactNode;
}

/** SchemaForm renders a form from a node type's JSON schema, so new node
 * types need no bespoke page (design: 属性面板据此自动生成表单). */
export function SchemaForm({ schema, value, onChange, widgets }: { schema: any; value: any; onChange: (v: any) => void; widgets: Widgets }) {
  return <ObjectFields schema={schema} value={value || {}} onChange={onChange} widgets={widgets} />;
}

function ObjectFields({ schema, value, onChange, widgets }: { schema: any; value: any; onChange: (v: any) => void; widgets: Widgets }) {
  const props = schema?.properties || {};
  return (
    <div className="col gap-s">
      {Object.entries<any>(props).map(([k, s]) => (
        <Field key={k} name={k} schema={s} value={value?.[k]} widgets={widgets} onChange={(v) => onChange({ ...value, [k]: v })} />
      ))}
      {schema?.additionalProperties && (
        <MapField value={value || {}} onChange={onChange} />
      )}
    </div>
  );
}

function MapField({ value, onChange }: { value: Record<string, string>; onChange: (v: any) => void }) {
  const entries = Object.entries(value);
  return (
    <div className="col gap-s">
      {entries.map(([k, v], i) => (
        <div className="row gap-s" key={i}>
          <input style={{ width: 90 }} value={k} onChange={(e) => {
            const next: Record<string, string> = {};
            entries.forEach(([kk, vv], j) => (next[j === i ? e.target.value : kk] = vv));
            onChange(next);
          }} />
          <input className="grow" value={v} onChange={(e) => onChange({ ...value, [k]: e.target.value })} />
          <button className="ghost" onClick={() => {
            const next = { ...value };
            delete next[k];
            onChange(next);
          }}>✕</button>
        </div>
      ))}
      <div>
        <button className="small" onClick={() => onChange({ ...value, ['arg' + (entries.length + 1)]: '' })}>+ 参数</button>
      </div>
    </div>
  );
}

function Field({ name, schema, value, onChange, widgets }: { name: string; schema: any; value: any; onChange: (v: any) => void; widgets: Widgets }) {
  const title = schema.title || name;
  const w = schema['x-widget'] as keyof Widgets | undefined;
  if (w && widgets[w]) {
    return (
      <label className="field">
        <span>{title}</span>
        {widgets[w](value, onChange)}
      </label>
    );
  }
  if (schema.type === 'object' && !schema.additionalProperties) {
    return (
      <fieldset style={{ border: '1px solid var(--line)', borderRadius: 6, padding: '6px 10px 10px' }}>
        <legend className="hint">{title}</legend>
        <ObjectFields schema={schema} value={value || {}} onChange={onChange} widgets={widgets} />
      </fieldset>
    );
  }
  if (schema.type === 'object') {
    return (
      <label className="field">
        <span>{title}</span>
        <MapField value={value || {}} onChange={onChange} />
      </label>
    );
  }
  if (schema.type === 'array') {
    const items: any[] = value || [];
    return (
      <fieldset style={{ border: '1px solid var(--line)', borderRadius: 6, padding: '6px 10px 10px' }}>
        <legend className="hint">{title}</legend>
        <div className="col gap-s">
          {items.map((it, i) => (
            <div key={i} style={{ borderLeft: '3px solid var(--line)', paddingLeft: 8 }}>
              <div className="row between">
                <span className="hint">#{i + 1}</span>
                <span>
                  <button className="ghost small" disabled={i === 0} onClick={() => {
                    const n = [...items];
                    [n[i - 1], n[i]] = [n[i], n[i - 1]];
                    onChange(n);
                  }}>↑</button>
                  <button className="ghost small" onClick={() => onChange(items.filter((_, j) => j !== i))}>删除</button>
                </span>
              </div>
              <ObjectFields schema={schema.items} value={it} widgets={widgets} onChange={(v) => onChange(items.map((x, j) => (j === i ? v : x)))} />
            </div>
          ))}
          <div>
            <button className="small" onClick={() => onChange([...items, {}])}>+ 添加</button>
          </div>
        </div>
      </fieldset>
    );
  }
  if (schema.type === 'boolean') {
    return (
      <label className="check">
        <input type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />
        {title}
      </label>
    );
  }
  if (schema.enum) {
    return (
      <label className="field">
        <span>{title}</span>
        <select value={value ?? schema.default ?? ''} onChange={(e) => onChange(e.target.value)}>
          <option value="">—</option>
          {schema.enum.map((o: string, i: number) => (
            <option key={o} value={o}>
              {schema.enumNames?.[i] || o}
            </option>
          ))}
        </select>
      </label>
    );
  }
  return (
    <label className="field">
      <span>{title}</span>
      <input
        type={schema.type === 'integer' || schema.type === 'number' ? 'number' : 'text'}
        value={value ?? ''}
        placeholder={schema.description || (schema.default !== undefined ? String(schema.default) : '')}
        onChange={(e) => onChange(schema.type === 'integer' || schema.type === 'number' ? (e.target.value === '' ? undefined : Number(e.target.value)) : e.target.value)}
      />
    </label>
  );
}
