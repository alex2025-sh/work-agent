import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { closeReasonText, fmtTime, get } from '../api';
import { useSession } from '../App';
import TaskDetail from '../components/TaskDetail';
import { RoundBadge } from '../components/ui';

interface Row {
  id: number;
  title: string;
  badge: string;
  summary: { field: string; value: string }[];
  status: string;
  close_reason: string;
  round: number;
  next_action_at: number;
  next_text: string;
  result: string;
  updated_at: number;
  attention: string[];
}

const groups: [string, string][] = [
  ['pending', '待处理'],
  ['waiting', '等待提醒'],
  ['done', '已结束'],
];

export default function MyTasks() {
  const { refreshKey, me } = useSession();
  const [params, setParams] = useSearchParams();
  const group = params.get('g') || 'pending';
  const [items, setItems] = useState<Row[]>([]);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [waiting, setWaiting] = useState<Row[]>([]);
  const sel = Number(params.get('t')) || 0;

  const load = useCallback(() => {
    get(`/api/my/tasks?group=${group}`).then((r) => {
      setItems(r.items || []);
      setCounts(r.counts || {});
      if (!sel && r.items?.length) setParams({ g: group, t: String(r.items[0].id) }, { replace: true });
    });
    if (group === 'pending') get('/api/my/tasks?group=waiting').then((r) => setWaiting(r.items || []));
  }, [group, sel, setParams]);
  useEffect(load, [load, refreshKey, me?.user.user_id]);

  const select = (id: number) => setParams({ g: group, t: String(id) });

  return (
    <div className="main">
      <div className="workbench">
        <div className="col">
          <div className="seg">
            {groups.map(([k, l]) => (
              <button key={k} className={group === k ? 'on' : ''} onClick={() => setParams({ g: k })}>
                {l} {k !== 'done' && counts[k] !== undefined ? counts[k] : ''}
              </button>
            ))}
          </div>
          <div className="task-list">
            {items.length === 0 && <div className="empty">没有任务</div>}
            {items.map((t) => (
              <div key={t.id} className={`task-item ${sel === t.id ? 'sel' : ''}`} onClick={() => select(t.id)}>
                <div className="row gap-s">
                  {group === 'done' ? <span className="pill">{closeReasonText[t.close_reason]}</span> : <RoundBadge label={t.badge} />}
                  <span className="title">{t.title}</span>
                </div>
                <div className="sub">{t.summary.map((s) => s.value).join(' · ')}</div>
                <div className="meta">
                  {group === 'waiting'
                    ? `${fmtTime(t.next_action_at)} 复查提醒`
                    : `${fmtTime(t.updated_at)}${t.round ? ` · 第 ${t.round} 轮` : ''}`}
                </div>
                {group === 'waiting' && t.result && <div className="meta">{t.result}</div>}
              </div>
            ))}
          </div>
          {group === 'pending' && waiting.length > 0 && (
            <div className="col gap-s">
              <div className="hint">等待下一次提醒</div>
              {waiting.slice(0, 3).map((t) => (
                <div key={t.id} className="task-item" style={{ background: 'var(--panel-2)' }} onClick={() => setParams({ g: 'waiting', t: String(t.id) })}>
                  <div className="row between">
                    <span className="sub">{t.summary[0]?.value}</span>
                    <span className="meta">{fmtTime(t.next_action_at)} 复查</span>
                  </div>
                  <div className="meta">{t.result}</div>
                </div>
              ))}
            </div>
          )}
        </div>
        <div>{sel ? <TaskDetail key={sel} id={sel} onChanged={load} /> : <div className="panel empty">选择左侧的任务</div>}</div>
      </div>
    </div>
  );
}
