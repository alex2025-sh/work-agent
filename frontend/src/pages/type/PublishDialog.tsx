import { useEffect, useState } from 'react';
import { ApiError, get, post } from '../../api';
import { Modal, useRun, useToast } from '../../components/ui';

const scopeText: Record<string, string> = { both: '一起发布（推荐）', config: '只发布采集配置', flow: '只发布处理流程' };
const icon: Record<string, string> = { pass: '✓', error: '✕', warning: '!', info: '·' };

export default function PublishDialog({ typeKey, typeName, onClose, onPublished }: { typeKey: string; typeName: string; onClose: () => void; onPublished: () => void }) {
  const [scope, setScope] = useState('both');
  const [plan, setPlan] = useState<any>(null);
  const [rescan, setRescan] = useState('continue');
  const [duration, setDuration] = useState('1d');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const run = useRun();

  useEffect(() => {
    setPlan(null);
    run(() => get(`/api/task-types/${typeKey}/publish-plan?scope=${scope}`)).then((p) => p && setPlan(p));
  }, [typeKey, scope, run]);

  const publish = async () => {
    setBusy(true);
    try {
      await post(`/api/task-types/${typeKey}/publish`, { scope, rescan, duration, note });
      toast('已发布：新版本只影响之后创建的任务');
      onPublished();
    } catch (e: any) {
      if (e instanceof ApiError && e.data) setPlan(e.data);
      toast(e.message, true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`发布 ${typeName}`}
      onClose={onClose}
      width={680}
      footer={
        <>
          <span className="hint grow">发布审核人由下一版角色与权限定义；当前由配置人员直接发布并留痕。</span>
          <button onClick={onClose}>取消</button>
          <button className="primary" disabled={!plan?.ok || busy} onClick={publish}>
            发布
          </button>
        </>
      }
    >
      <div className="col">
        <div className="hint">新版本只影响之后创建的任务</div>
        <div>
          <h3 className="mb">发布范围</h3>
          {(plan?.options || [{ scope: 'both', available: true }, { scope: 'config', available: true }, { scope: 'flow', available: true }]).map((o: any) => (
            <label key={o.scope} className="check" style={{ display: 'flex', padding: '4px 0', opacity: o.available ? 1 : 0.55 }}>
              <input type="radio" checked={scope === o.scope} onChange={() => setScope(o.scope)} />
              <span>
                <b>{scopeText[o.scope]}</b>
                <span className="hint" style={{ marginLeft: 8 }}>
                  {o.scope === 'both' && plan ? `采集配置 r${plan.config_from} → v${plan.config_to || '?'} · 处理流程 r${plan.flow_from} → v${plan.flow_to || '?'}，同时校验、同时生效` : o.reason}
                </span>
              </span>
            </label>
          ))}
        </div>
        {!plan && <div className="hint">正在执行发布前检查…</div>}
        {plan && (
          <>
            <div>
              <h3 className="mb">发布前检查</h3>
              <ul className="checks">
                {plan.checks.map((c: any, i: number) => (
                  <li key={i} className={c.level}>
                    <span className="ic">{icon[c.level]}</span>
                    {c.text}
                  </li>
                ))}
              </ul>
            </div>
            {plan.dry_run && (
              <div className="row">
                <div className="panel tight grow">
                  <div className="hint">按当前数据，生效后首次扫描预计新建</div>
                  <div style={{ fontSize: 22, fontWeight: 700 }}>{plan.dry_run.new_tasks} 个任务</div>
                </div>
                <div className="panel tight grow">
                  <div className="hint">预计通知</div>
                  <div style={{ fontSize: 22, fontWeight: 700 }}>{plan.dry_run.notify_rms} 位 RM</div>
                </div>
              </div>
            )}
            <div className="hint">
              在途任务继续使用 配置 v{plan.active_config || '—'} · 流程 v{plan.active_flow || '—'}
            </div>
            {scope !== 'flow' && (
              <div className="row">
                <label className="field">
                  <span>扫描水位</span>
                  <select value={rescan} onChange={(e) => setRescan(e.target.value)}>
                    <option value="continue">从当前水位继续</option>
                    <option value="duration">按指定时长回扫</option>
                  </select>
                </label>
                {rescan === 'duration' && (
                  <label className="field">
                    <span>回扫时长</span>
                    <input value={duration} onChange={(e) => setDuration(e.target.value)} style={{ width: 90 }} />
                  </label>
                )}
              </div>
            )}
            <label className="field">
              <span>发布说明</span>
              <input value={note} onChange={(e) => setNote(e.target.value)} placeholder="如：字段映射新增 vip.source" />
            </label>
          </>
        )}
      </div>
    </Modal>
  );
}
