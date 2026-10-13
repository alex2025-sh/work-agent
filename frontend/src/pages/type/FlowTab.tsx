import { DragEvent, memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import ReactFlow, {
  Background,
  Connection,
  Controls,
  Edge,
  Handle,
  MarkerType,
  MiniMap,
  Node,
  NodeProps,
  NodeResizer,
  NodeToolbar,
  Position,
  ReactFlowInstance,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
  useUpdateNodeInternals,
} from 'reactflow';
import { get, post, put } from '../../api';
import { useSession } from '../../App';
import { ConditionEditor, SchemaForm, useRun } from '../../components/ui';
import type { TypeInfo } from '../TaskTypeDetail';
import { defaultConfig, EngineNode, EXTENSIONS, exitLabel, exitsOf, Graph, Issue, NodeMeta, summary, TAG } from './flowModel';

interface BwData {
  node: EngineNode;
  meta?: NodeMeta;
  issues: Issue[];
  onDelete: (id: string) => void;
  onDuplicate: (id: string) => void;
}

const BwNode = memo(({ id, data, selected }: NodeProps<BwData>) => {
  const n = data.node;
  const exits = exitsOf(n);
  const update = useUpdateNodeInternals();
  const sig = exits.join('|');
  useEffect(() => update(id), [id, sig, update]);
  const color = data.meta?.color || '#6b7280';
  const hasErr = data.issues.some((i) => i.level === 'error');
  return (
    <div className={`bw-node ${selected ? 'selected' : ''} ${hasErr ? 'err' : ''}`}>
      <NodeToolbar isVisible={selected && n.type !== 'start'} position={Position.Top}>
        <div className="row gap-s">
          <button className="small" onClick={() => data.onDuplicate(id)}>
            复制
          </button>
          <button className="small danger" onClick={() => data.onDelete(id)}>
            删除
          </button>
        </div>
      </NodeToolbar>
      {n.type !== 'start' && <Handle type="target" position={Position.Top} />}
      <div className="nh">
        <span className="tag" style={{ background: color }}>
          {TAG[n.type] || n.type}
        </span>
        <span className="hint mono">{n.key}</span>
        {hasErr && <span className="pill red">!</span>}
      </div>
      <div className="nt">{n.name || data.meta?.title}</div>
      <div className="ns" style={{ whiteSpace: 'pre-line' }}>
        {summary(n)}
      </div>
      {exits.length > 0 && (
        <div className="exits">
          {exits.map((e) => (
            <span key={e}>{exitLabel(n, e) || '→'}</span>
          ))}
        </div>
      )}
      {exits.map((e, i) => (
        <Handle key={e} id={e} type="source" position={Position.Bottom} style={{ left: `${((i + 1) * 100) / (exits.length + 1)}%`, background: color }} />
      ))}
    </div>
  );
});

interface NoteData {
  text: string;
  onText: (id: string, t: string) => void;
}

/** Sticky note: UI-only annotation kept in layout._notes (never in graph). */
const NoteNode = memo(({ id, data, selected }: NodeProps<NoteData>) => (
  <>
    <NodeResizer isVisible={selected} minWidth={140} minHeight={60} />
    <div className="bw-note">
      <textarea value={data.text} placeholder="备注" onChange={(e) => data.onText(id, e.target.value)} />
    </div>
  </>
));

const nodeTypes = { bw: BwNode, note: NoteNode };

function FlowEditor({ typeKey, info, onPublish, reload }: { typeKey: string; info: TypeInfo; onPublish: () => void; reload: number }) {
  const { me } = useSession();
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  const [metas, setMetas] = useState<Record<string, NodeMeta>>({});
  const [caps, setCaps] = useState<any[]>([]);
  const [draft, setDraft] = useState<any>(null);
  const [issues, setIssues] = useState<Issue[]>([]);
  const [inputs, setInputs] = useState<string[]>([]);
  const [produced, setProduced] = useState<string[]>([]);
  const [selId, setSelId] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const rf = useRef<ReactFlowInstance | null>(null);
  const run = useRun();
  const editable = me?.user.role === 'config' || me?.user.role === 'admin';
  const cards = useMemo(() => Object.keys(draft?.cards || {}), [draft]);

  const callbacks = useRef({
    onDelete: (id: string) => {
      setNodes((ns) => ns.filter((n) => n.id !== id));
      setEdges((es) => es.filter((e) => e.source !== id && e.target !== id));
      setSelId(null);
      setDirty(true);
    },
    onDuplicate: (id: string) => {
      setNodes((ns) => {
        const src = ns.find((n) => n.id === id);
        if (!src) return ns;
        const node = src.data.node as EngineNode;
        const key = uniqueKey(ns, node.type);
        return [...ns.map((n) => ({ ...n, selected: false })), mkNode({ ...node, key, name: node.name + ' 副本' }, { x: src.position.x + 40, y: src.position.y + 40 }, src.data.meta, true)];
      });
      setDirty(true);
    },
    onText: (id: string, text: string) => {
      setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, data: { ...n.data, text } } : n)));
      setDirty(true);
    },
  });

  const mkNode = useCallback((n: EngineNode, pos: { x: number; y: number }, meta?: NodeMeta, selected = false): Node => ({
    id: n.key,
    type: 'bw',
    position: pos,
    selected,
    deletable: n.type !== 'start',
    data: { node: n, meta, issues: [], onDelete: callbacks.current.onDelete, onDuplicate: callbacks.current.onDuplicate },
  }), []);

  const mkEdge = (from: string, exit: string, to: string, label: string): Edge => ({
    id: `${from}:${exit}`,
    source: from,
    sourceHandle: exit,
    target: to,
    label,
    markerEnd: { type: MarkerType.ArrowClosed },
    style: { strokeWidth: 1.5 },
  });

  // load node types, capabilities, draft
  useEffect(() => {
    Promise.all([get<NodeMeta[]>('/api/node-types'), get('/api/capabilities'), get(`/api/task-types/${typeKey}/flow-draft`), get(`/api/task-types/${typeKey}/config-draft`)]).then(
      ([types, cs, fd, cd]) => {
        const m: Record<string, NodeMeta> = {};
        types.forEach((t) => (m[t.type] = t));
        setMetas(m);
        setCaps(cs);
        const def = fd.definition;
        setDraft(def);
        setIssues(fd.validation.issues || []);
        setInputs(fd.validation.inputs || []);
        setProduced((cd.spec.fields || []).map((f: any) => f.field));
        const layout = def.layout || {};
        const g: Graph = def.graph;
        const ns: Node[] = g.nodes.map((n, i) => mkNode(n, layout[n.key] || { x: 0, y: i * 140 }, m[n.type]));
        for (const note of layout._notes || []) {
          ns.push({ id: note.id, type: 'note', position: { x: note.x, y: note.y }, style: { width: note.w, height: note.h }, data: { text: note.text, onText: callbacks.current.onText } });
        }
        setNodes(ns);
        setEdges(g.edges.map((e) => mkEdge(e.from, e.exit, e.to, exitLabel(g.nodes.find((x) => x.key === e.from)!, e.exit))));
        setDirty(false);
      },
    );
  }, [typeKey, reload, mkNode, setNodes, setEdges]);

  const toGraph = useCallback((): { graph: Graph; layout: any } => {
    const bw = nodes.filter((n) => n.type === 'bw');
    const graph: Graph = {
      nodes: bw.map((n) => n.data.node as EngineNode),
      edges: edges.map((e) => ({ from: e.source, exit: e.sourceHandle || 'next', to: e.target })),
    };
    const layout: any = {};
    bw.forEach((n) => (layout[n.id] = { x: Math.round(n.position.x), y: Math.round(n.position.y) }));
    layout._notes = nodes
      .filter((n) => n.type === 'note')
      .map((n) => ({ id: n.id, x: Math.round(n.position.x), y: Math.round(n.position.y), w: Number(n.width || (n.style as any)?.width || 200), h: Number(n.height || (n.style as any)?.height || 100), text: n.data.text }));
    return { graph, layout };
  }, [nodes, edges]);

  // live validation (graph only — layout never affects validation)
  const graphSig = useMemo(() => JSON.stringify(toGraph().graph), [toGraph]);
  useEffect(() => {
    if (!draft) return;
    const h = setTimeout(() => {
      post(`/api/task-types/${typeKey}/flow-validate`, { graph: JSON.parse(graphSig), cards: draft.cards }).then((v) => {
        setIssues(v.issues || []);
        setInputs(v.inputs || []);
      });
    }, 400);
    return () => clearTimeout(h);
  }, [graphSig, draft, typeKey]);

  // decorate nodes with their issues
  useEffect(() => {
    setNodes((ns) =>
      ns.map((n) => {
        if (n.type !== 'bw') return n;
        const mine = issues.filter((i) => i.node === n.id);
        if (JSON.stringify(mine) === JSON.stringify(n.data.issues)) return n;
        return { ...n, data: { ...n.data, issues: mine } };
      }),
    );
  }, [issues, setNodes]);

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return;
      const src = nodes.find((n) => n.id === c.source);
      const exit = c.sourceHandle || 'next';
      setEdges((es) => [...es.filter((e) => !(e.source === c.source && e.sourceHandle === exit)), mkEdge(c.source!, exit, c.target!, src ? exitLabel(src.data.node, exit) : exit)]);
      setDirty(true);
    },
    [nodes, setEdges],
  );

  const updateNode = (id: string, patch: Partial<EngineNode>) => {
    setNodes((ns) =>
      ns.map((n) => {
        if (n.id !== id) return n;
        const node = { ...n.data.node, ...patch } as EngineNode;
        return { ...n, data: { ...n.data, node } };
      }),
    );
    // keep edge labels and drop edges whose exit disappeared
    setEdges((es) => {
      const n = nodes.find((x) => x.id === id);
      if (!n) return es;
      const node = { ...n.data.node, ...patch } as EngineNode;
      const exits = exitsOf(node);
      return es.filter((e) => e.source !== id || exits.includes(e.sourceHandle || '')).map((e) => (e.source === id ? { ...e, label: exitLabel(node, e.sourceHandle || '') } : e));
    });
    setDirty(true);
  };

  const renameKey = (oldKey: string, newKey: string) => {
    if (!newKey || nodes.some((n) => n.id === newKey)) return;
    setNodes((ns) => ns.map((n) => (n.id === oldKey ? { ...n, id: newKey, data: { ...n.data, node: { ...n.data.node, key: newKey } } } : n)));
    setEdges((es) =>
      es.map((e) => {
        const source = e.source === oldKey ? newKey : e.source;
        const target = e.target === oldKey ? newKey : e.target;
        return { ...e, id: `${source}:${e.sourceHandle}`, source, target };
      }),
    );
    setSelId(newKey);
    setDirty(true);
  };

  const onDrop = (ev: DragEvent) => {
    ev.preventDefault();
    const type = ev.dataTransfer.getData('application/bywork-node');
    if (!type || !rf.current) return;
    const pos = rf.current.screenToFlowPosition({ x: ev.clientX, y: ev.clientY });
    if (type === 'note') {
      setNodes((ns) => [...ns, { id: 'note_' + Date.now().toString(36), type: 'note', position: pos, style: { width: 200, height: 90 }, data: { text: '', onText: callbacks.current.onText } }]);
    } else {
      const key = uniqueKey(nodes, type);
      const meta = metas[type];
      const node: EngineNode = { key, type, name: meta?.title || type, config: defaultConfig(type, info.type.team, cards[0] || '') };
      setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), mkNode(node, pos, meta, true)]);
      setSelId(key);
    }
    setDirty(true);
  };

  const save = async () => {
    const { graph, layout } = toGraph();
    const r = await run(() => put(`/api/task-types/${typeKey}/flow-draft`, { graph, layout, cards: draft.cards, note: draft.note }), '流程草稿已保存');
    if (r) {
      setDraft(r.definition);
      setIssues(r.validation.issues || []);
      setInputs(r.validation.inputs || []);
      setDirty(false);
    }
  };

  const sel = nodes.find((n) => n.id === selId && n.type === 'bw');
  const allFields = useMemo(() => {
    const out = new Set(produced);
    caps.forEach((c) => c.outputs.forEach((o: string) => out.add(o)));
    return [...out].sort();
  }, [produced, caps]);
  const errors = issues.filter((i) => i.level === 'error');

  const widgets = {
    conditions: (v: any, set: (v: any) => void) => <ConditionEditor value={v || { all: [] }} onChange={set} fields={allFields} />,
    capability: (v: any, set: (v: any) => void) => (
      <div className="col gap-s">
        <select value={v || ''} onChange={(e) => set(e.target.value)}>
          <option value="">选择 data.* 能力</option>
          {caps.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name} · {c.title}
            </option>
          ))}
        </select>
        {v && <span className="hint">产出：{caps.find((c) => c.name === v)?.outputs.join('、')}</span>}
      </div>
    ),
    card: (v: any, set: (v: any) => void) => (
      <select value={v || ''} onChange={(e) => set(e.target.value)}>
        <option value="">选择卡片模板</option>
        {cards.map((c) => (
          <option key={c} value={c}>
            {draft.cards[c].name || c}
          </option>
        ))}
      </select>
    ),
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
      <div className="row between" style={{ padding: '8px 20px', background: 'var(--panel)', borderTop: '1px solid var(--line)' }}>
        <div className="row">
          <b>
            流程草稿 r{draft?.revision ?? '—'} · 基于 v{draft?.base_version || '—'}
          </b>
          {dirty && <span className="pill amber">未保存</span>}
          {draft?.note && <span className="hint">改动：{draft.note}</span>}
        </div>
        <div className="row gap-s">
          <span className={`pill ${errors.length ? 'red' : 'green'}`}>{errors.length ? `${errors.length} 个错误` : '校验通过'}</span>
          <button disabled={!editable} onClick={save}>
            保存草稿
          </button>
          <button className="primary" disabled={dirty} title={dirty ? '请先保存草稿' : ''} onClick={onPublish}>
            发布…
          </button>
        </div>
      </div>
      <div className="flow-shell">
        <div className="flow-side">
          <h3 className="mb">节点库</h3>
          <div className="hint mb">一期 · 拖到画布</div>
          {['human', 'wait', 'condition', 'data', 'end'].map((t) => (
            <div key={t} className="palette-item" draggable onDragStart={(e) => e.dataTransfer.setData('application/bywork-node', t)} title={metas[t]?.desc}>
              <span className="dot" style={{ background: metas[t]?.color }} />
              {metas[t]?.title || t}
            </div>
          ))}
          <div className="palette-item" draggable onDragStart={(e) => e.dataTransfer.setData('application/bywork-node', 'note')}>
            <span className="dot" style={{ background: '#ecd98a' }} />
            备注（不进入流程）
          </div>
          <div className="hint mb mt">扩展 · 后续开放</div>
          {EXTENSIONS.map((x) => (
            <div key={x} className="palette-item disabled">
              <span className="dot" style={{ background: '#ccc' }} />
              {x}
            </div>
          ))}
        </div>
        <div className="flow-canvas" onDragOver={(e) => e.preventDefault()} onDrop={onDrop}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onInit={(i) => (rf.current = i)}
            onNodesChange={(c) => {
              onNodesChange(c);
              // selection and measuring are not edits; moving, resizing notes and deleting are
              if (c.some((x) => (x.type === 'position' && x.dragging) || x.type === 'remove' || (x.type === 'dimensions' && x.resizing))) setDirty(true);
            }}
            onEdgesChange={(c) => {
              onEdgesChange(c);
              if (c.some((x) => x.type === 'remove')) setDirty(true);
            }}
            onConnect={onConnect}
            onSelectionChange={({ nodes: s }) => setSelId(s.length === 1 ? s[0].id : null)}
            fitView
            fitViewOptions={{ padding: 0.2 }}
            deleteKeyCode={['Delete']}
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={16} color="#ddd8cc" />
            <Controls />
            <MiniMap pannable zoomable nodeColor={(n) => (n.type === 'note' ? '#ecd98a' : (n.data as BwData).meta?.color || '#999')} />
          </ReactFlow>
        </div>
        <div className="flow-side right">
          {sel ? (
            <NodePanel
              key={sel.id}
              node={sel.data.node}
              meta={metas[sel.data.node.type]}
              issues={issues.filter((i) => i.node === sel.id)}
              onChange={(p) => updateNode(sel.id, p)}
              onRename={(k) => renameKey(sel.id, k)}
              widgets={widgets}
              caps={caps}
            />
          ) : (
            <ContractPanel inputs={inputs} produced={produced} issues={issues} />
          )}
        </div>
      </div>
    </div>
  );
}

function NodePanel({ node, meta, issues, onChange, onRename, widgets, caps }: { node: EngineNode; meta?: NodeMeta; issues: Issue[]; onChange: (p: Partial<EngineNode>) => void; onRename: (k: string) => void; widgets: any; caps: any[] }) {
  const [key, setKey] = useState(node.key);
  const refs = useMemo(() => {
    const s = JSON.stringify(node.config || {});
    return [...new Set([...s.matchAll(/([a-z_]+\.[a-z_.]+)/g)].map((m) => m[1]).filter((f) => /^(case|vip|rm)\./.test(f)))];
  }, [node.config]);
  return (
    <div className="col">
      <div>
        <span className="tag pill" style={{ background: meta?.color, color: '#fff', borderColor: meta?.color }}>
          {TAG[node.type]}
        </span>{' '}
        <b>{meta?.title}</b>
        <div className="hint">{meta?.desc}</div>
      </div>
      <label className="field">
        <span>名称</span>
        <input value={node.name} onChange={(e) => onChange({ name: e.target.value })} />
      </label>
      <label className="field">
        <span>Key</span>
        <input className="mono" value={key} onChange={(e) => setKey(e.target.value)} onBlur={() => key !== node.key && onRename(key)} />
      </label>
      {issues.length > 0 && (
        <div className="issues">
          {issues.map((i, j) => (
            <div key={j} className={`issue ${i.level}`}>
              {i.level === 'error' ? '✕' : i.level === 'warning' ? '!' : 'i'} {i.message}
            </div>
          ))}
        </div>
      )}
      {meta && <SchemaForm schema={meta.schema} value={node.config || {}} onChange={(config) => onChange({ config })} widgets={widgets} />}
      {node.type === 'data' && node.config?.capability && (
        <div className="hint">实时读取，受数据权限管控，不进入字段契约。产出：{caps.find((c) => c.name === node.config.capability)?.outputs.join('、')}</div>
      )}
      {refs.length > 0 && <div className="hint">引用字段：{refs.join('、')}</div>}
    </div>
  );
}

function ContractPanel({ inputs, produced, issues }: { inputs: string[]; produced: string[]; issues: Issue[] }) {
  return (
    <div className="col">
      <h3>流程输入契约</h3>
      <div className="hint">绑定此流程的任务类型必须提供</div>
      <div className="kvline">
        {inputs.map((f) => (
          <span key={f} className={`pill ${produced.includes(f) ? 'green' : 'red'}`} title={produced.includes(f) ? '当前配置草稿产出' : '当前配置草稿不产出'}>
            {f}
          </span>
        ))}
      </div>
      <h3 className="mt">校验</h3>
      <div className="issues" style={{ maxHeight: 'none' }}>
        {issues.length === 0 && <div className="hint">—</div>}
        {issues.map((i, j) => (
          <div key={j} className={`issue ${i.level}`}>
            {i.level === 'error' ? '✕' : i.level === 'warning' ? '!' : '✓'} {i.node && <code>{i.node}</code>} {i.message}
          </div>
        ))}
      </div>
      <div className="hint mt">选中节点编辑属性。拖动节点下方的出口圆点连线；每个出口只能连一条线。等待之后的判断必须先用取数节点实时读取。</div>
    </div>
  );
}

function uniqueKey(ns: Node[], type: string) {
  let i = 1;
  while (ns.some((n) => n.id === `${type}_${i}`)) i++;
  return `${type}_${i}`;
}

export default function FlowTab(props: { typeKey: string; info: TypeInfo; onPublish: () => void; reload: number }) {
  return (
    <ReactFlowProvider>
      <FlowEditor {...props} />
    </ReactFlowProvider>
  );
}
