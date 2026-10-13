import { useEffect, useState } from 'react';
import { fmtTime, get, post } from '../../api';
import { useRun } from '../../components/ui';
import { lineDiff as diff } from '../../components/diff';
import type { TypeInfo } from '../TaskTypeDetail';

export default function VersionsTab({ typeKey, info, onChanged }: { typeKey: string; info: TypeInfo; onChanged: () => void }) {
  const [configs, setConfigs] = useState<any[]>([]);
  const [flows, setFlows] = useState<any[]>([]);
  const [cmp, setCmp] = useState<{ title: string; a: string; b: string } | null>(null);
  const run = useRun();
  const load = () => {
    get(`/api/task-types/${typeKey}/configs`).then(setConfigs);
    get(`/api/task-types/${typeKey}/flows`).then(setFlows);
  };
  useEffect(load, [typeKey]);
  const t = info.type;

  const list = (kind: 'config' | 'flow', rows: any[], active: number, body: (r: any) => any) => (
    <div className="panel grow">
      <div className="panel-title">
        <h2>{kind === 'config' ? '采集配置' : '处理流程（含卡片）'}</h2>
        <span className="hint">生效 v{active || '—'}</span>
      </div>
      <table className="tbl">
        <thead>
          <tr>
            <th>版本</th>
            <th>发布</th>
            <th>作者</th>
            <th>说明</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, idx) => {
            const prev = rows.slice(idx + 1).find((x) => x.version > 0) || (r.version === 0 ? rows.find((x) => x.version === active) : null);
            return (
              <tr key={r.id}>
                <td>
                  {r.version === 0 ? <span className="pill amber">草稿 r{r.revision}</span> : <b>v{r.version}</b>} {r.version === active && <span className="pill green">生效</span>}
                </td>
                <td>{r.version ? fmtTime(r.published_at) : fmtTime(r.updated_at)}</td>
                <td>{r.author}</td>
                <td className="hint">{r.note}</td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  {prev && (
                    <button className="ghost small" onClick={() => setCmp({ title: `${kind} v${prev.version} → ${r.version ? 'v' + r.version : '草稿'}`, a: JSON.stringify(body(prev), null, 2), b: JSON.stringify(body(r), null, 2) })}>
                      与 v{prev.version} 对比
                    </button>
                  )}
                  {r.version > 0 && r.version !== active && (
                    <button className="ghost small" onClick={async () => {
                      if (!window.confirm(`把 v${r.version} 复制为新版本并发布？（不改写历史版本）`)) return;
                      const res = await run(() => post(`/api/task-types/${typeKey}/rollback`, { kind, version: r.version }), '已回滚为新版本');
                      if (res) {
                        load();
                        onChanged();
                      }
                    }}>
                      回滚到此版本
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );

  return (
    <div className="col" style={{ maxWidth: 1260, margin: '0 auto' }}>
      <div className="hint">TaskConfig 和 Workflow 各自版本化。新版本只影响之后创建的任务；在途任务按创建时锁定的版本走完。回滚 = 把旧版本复制为新版本再发布。</div>
      <div className="row" style={{ alignItems: 'flex-start' }}>
        {list('config', configs, t.active_config_version, (r) => r.spec)}
        {list('flow', flows, t.active_flow_version, (r) => ({ graph: r.graph, cards: r.cards }))}
      </div>
      {cmp && (
        <div className="panel">
          <div className="panel-title row between">
            <h2>{cmp.title}</h2>
            <button className="ghost" onClick={() => setCmp(null)}>
              关闭
            </button>
          </div>
          <div className="hint mb">只比较 graph 与配置，布局坐标不参与对比</div>
          <div className="diff">
            {diff(cmp.a, cmp.b)
              .filter((l, i, arr) => l.t !== ' ' || arr.slice(Math.max(0, i - 2), i + 3).some((x) => x.t !== ' '))
              .map((l, i) => (
                <span key={i} className={l.t === '+' ? 'add' : l.t === '-' ? 'rm' : ''}>
                  {l.t} {l.s}
                </span>
              ))}
          </div>
        </div>
      )}
    </div>
  );
}
