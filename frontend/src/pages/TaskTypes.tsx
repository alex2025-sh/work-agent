import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { fmtTime, get, post } from '../api';
import { useSession } from '../App';
import { Modal, TypeStatusPill, useRun } from '../components/ui';

export default function TaskTypes() {
  const { refreshKey, me } = useSession();
  const [types, setTypes] = useState<any[]>([]);
  const [creating, setCreating] = useState(false);
  const nav = useNavigate();
  useEffect(() => {
    get('/api/task-types').then(setTypes);
  }, [refreshKey]);
  const canCreate = me?.user.role === 'config' || me?.user.role === 'admin';
  return (
    <div className="main">
      <div className="col" style={{ maxWidth: 1100, margin: '0 auto' }}>
        <div className="row between">
          <div>
            <h1>任务类型</h1>
            <div className="hint">任务类型定义"这是什么事、什么时候产生"，流程定义"怎么处理"。</div>
          </div>
          {canCreate && (
            <button className="primary" onClick={() => setCreating(true)}>
              新建任务类型
            </button>
          )}
        </div>
        <div className="panel">
          <table className="tbl">
            <thead>
              <tr>
                <th>名称</th>
                <th>Key</th>
                <th>状态</th>
                <th>生效版本</th>
                <th>负责团队</th>
                <th>去重</th>
                <th>创建者</th>
                <th>更新</th>
              </tr>
            </thead>
            <tbody>
              {types.map((t) => (
                <tr key={t.type_key} className="click" onClick={() => nav(`/types/${t.type_key}`)}>
                  <td style={{ fontWeight: 600 }}>{t.name}</td>
                  <td className="mono">{t.type_key}</td>
                  <td>
                    <TypeStatusPill status={t.status} />
                  </td>
                  <td>
                    配置 v{t.active_config_version || '—'} · 流程 v{t.active_flow_version || '—'}
                  </td>
                  <td>{t.team}</td>
                  <td className="mono hint">{t.round_field ? `+ ${t.round_field}` : ''}</td>
                  <td>{t.creator}</td>
                  <td>{fmtTime(t.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
      {creating && <CreateType onClose={() => setCreating(false)} onCreated={(k) => nav(`/types/${k}`)} />}
    </div>
  );
}

function CreateType({ onClose, onCreated }: { onClose: () => void; onCreated: (k: string) => void }) {
  const [f, setF] = useState({ type_key: '', name: '', team: 'VIP_RM', object_type: 'CRM Case', description: '', dedupe: 'case.appeal_id', round_field: '' });
  const run = useRun();
  const submit = async () => {
    const r = await run(() =>
      post('/api/task-types', {
        type_key: f.type_key,
        name: f.name,
        team: f.team,
        object_type: f.object_type,
        description: f.description,
        dedupe_fields: f.dedupe.split(',').map((s) => s.trim()).filter(Boolean),
        round_field: f.round_field.trim(),
      }),
      '已创建：草稿状态',
    );
    if (r) onCreated(f.type_key);
  };
  const input = (k: keyof typeof f, label: string, ph = '') => (
    <label className="field">
      <span>{label}</span>
      <input value={f[k]} placeholder={ph} onChange={(e) => setF({ ...f, [k]: e.target.value })} />
    </label>
  );
  return (
    <Modal
      title="新建任务类型"
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose}>取消</button>
          <button className="primary" onClick={submit}>
            创建
          </button>
        </>
      }
    >
      <div className="col">
        {input('type_key', 'Key（创建后不可改）', 'VIP_SOMETHING')}
        {input('name', '名称')}
        {input('team', '负责团队')}
        {input('object_type', '对象类型')}
        {input('description', '说明')}
        {input('dedupe', '去重键字段（逗号分隔）', 'case.appeal_id')}
        {input('round_field', '轮次字段（可选）', 'case.maker_review_entry_id')}
        <div className="hint">去重定义放在任务类型上：改配置不会让同一个对象被重复提醒。确实要改去重方式，请新建任务类型。</div>
      </div>
    </Modal>
  );
}
