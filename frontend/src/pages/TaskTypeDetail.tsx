import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { fmtTime, get, post } from '../api';
import { useSession } from '../App';
import { Modal, TypeStatusPill, useRun } from '../components/ui';
import ConfigTab from './type/ConfigTab';
import FlowTab from './type/FlowTab';
import CardTab from './type/CardTab';
import VersionsTab from './type/VersionsTab';
import RecordsTab from './type/RecordsTab';
import PublishDialog from './type/PublishDialog';
import AIAssistant from '../components/AIAssistant';

const tabs: [string, string][] = [
  ['config', '采集配置'],
  ['flow', '处理流程'],
  ['card', '卡片'],
  ['versions', '版本'],
  ['records', '任务记录'],
];

export interface TypeInfo {
  type: any;
  dedupe_fields: string[];
  schedule: any;
}

export default function TaskTypeDetail() {
  const { key = '', tab = 'config' } = useParams();
  const nav = useNavigate();
  const { refreshKey, me } = useSession();
  const canConfigure = me?.user.role === 'config' || me?.user.role === 'admin';
  const [info, setInfo] = useState<TypeInfo | null>(null);
  const [publishing, setPublishing] = useState(false);
  const [assistant, setAssistant] = useState(false);
  const [reload, setReload] = useState(0);
  const load = useCallback(() => {
    get<TypeInfo>(`/api/task-types/${key}`).then(setInfo);
  }, [key]);
  useEffect(load, [load, refreshKey, reload]);
  if (!info) return <div className="main hint">加载中…</div>;
  const t = info.type;
  const refresh = () => setReload((x) => x + 1);

  return (
    <div className="main flush">
      <div style={{ background: 'var(--panel)', padding: '14px 20px 0' }}>
        <div className="hint">
          <a onClick={() => nav('/types')} style={{ cursor: 'pointer' }}>
            任务类型
          </a>{' '}
          /
        </div>
        <div className="row between">
          <div className="row">
            <h1>{t.name}</h1>
            <code>{t.type_key}</code>
            <TypeStatusPill status={t.status} />
            <span className="hint">
              生效：配置 v{t.active_config_version || '—'} · 流程 v{t.active_flow_version || '—'}
            </span>
            <span className="hint">创建者 {t.creator}</span>
          </div>
          <div className="row gap-s">
            <Lifecycle t={t} onDone={refresh} />
            {canConfigure && <button onClick={() => setAssistant(true)} title="用对话修改采集配置和处理流程，确认 diff 后写入草稿">
              AI 助手
            </button>}
            <button className="primary" onClick={() => setPublishing(true)}>
              发布…
            </button>
          </div>
        </div>
        <div className="tabs" style={{ marginTop: 10, borderBottom: 'none' }}>
          {tabs.map(([k, l]) => (
            <button key={k} className={tab === k ? 'on' : ''} onClick={() => nav(`/types/${key}/${k}`)}>
              {l}
            </button>
          ))}
        </div>
      </div>
      {tab === 'flow' ? (
        <FlowTab typeKey={key} info={info} onPublish={() => setPublishing(true)} reload={reload} />
      ) : (
        <div style={{ overflow: 'auto', flex: 1, padding: 20, borderTop: '1px solid var(--line)' }}>
          {tab === 'config' && <ConfigTab typeKey={key} info={info} onPublish={() => setPublishing(true)} onChanged={refresh} reload={reload} />}
          {tab === 'card' && <CardTab typeKey={key} onPublish={() => setPublishing(true)} reload={reload} />}
          {tab === 'versions' && <VersionsTab typeKey={key} info={info} onChanged={refresh} />}
          {tab === 'records' && <RecordsTab typeKey={key} info={info} />}
        </div>
      )}
      {assistant && <AIAssistant typeKey={key} onClose={() => setAssistant(false)} onAccepted={refresh} />}
      {publishing && (
        <PublishDialog
          typeKey={key}
          typeName={t.name}
          onClose={() => setPublishing(false)}
          onPublished={() => {
            setPublishing(false);
            refresh();
          }}
        />
      )}
    </div>
  );
}

/** Lifecycle: 草稿 → 影子运行（可选）→ 启用 ⇄ 停用 */
function Lifecycle({ t, onDone }: { t: any; onDone: () => void }) {
  const run = useRun();
  const [enable, setEnable] = useState(false);
  const [start, setStart] = useState('from_now');
  const [dur, setDur] = useState('7d');
  const act = async (action: string, extra: any = {}) => {
    const r = await run(() => post(`/api/task-types/${t.type_key}/status`, { action, ...extra }), '状态已更新');
    if (r) onDone();
  };
  const firstEnable = !t.enabled_at;
  return (
    <>
      {(t.status === 'draft' || t.status === 'disabled') && (
        <button onClick={() => act('start_shadow')} title="任务照常创建、流程照常执行，但不推送、不进入任何人的待办">
          开始影子运行
        </button>
      )}
      {t.status !== 'enabled' && <button onClick={() => (firstEnable ? setEnable(true) : act('enable'))}>{t.status === 'shadow' ? '结束影子运行并启用' : '启用'}</button>}
      {(t.status === 'enabled' || t.status === 'shadow') && (
        <button className="danger" onClick={() => window.confirm('停用后已有任务保留，不再新建和推送。确定？') && act('disable')}>
          停用
        </button>
      )}
      {enable && (
        <Modal
          title="首次启用：选择起点"
          onClose={() => setEnable(false)}
          footer={
            <>
              <button onClick={() => setEnable(false)}>取消</button>
              <button className="primary" onClick={() => { setEnable(false); act('enable', { start, duration: dur }); }}>
                启用
              </button>
            </>
          }
        >
          <div className="col">
            <div className="hint">存量对象可能很多，会一次性推给 RM。启用时必须选择起点；超出单次上限时熔断。</div>
            <label className="check">
              <input type="radio" checked={start === 'from_now'} onChange={() => setStart('from_now')} />
              只处理启用之后进入阶段的对象
            </label>
            <label className="check">
              <input type="radio" checked={start === 'duration'} onChange={() => setStart('duration')} />
              回扫最近
              <input style={{ width: 70 }} value={dur} onChange={(e) => setDur(e.target.value)} />
            </label>
            {t.status === 'shadow' && <div className="hint">影子任务将全部以「影子运行结束」关闭，不推送。</div>}
            <div className="hint">影子运行开始于 {fmtTime(t.shadow_started_at)}</div>
          </div>
        </Modal>
      )}
    </>
  );
}
