import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import { BrowserRouter, Navigate, NavLink, Route, Routes } from 'react-router-dom';
import { currentUser, fmtTime, get, post, setCurrentUser } from './api';
import { ToastProvider, useRun } from './components/ui';
import MyTasks from './pages/MyTasks';
import LeaderQueue from './pages/LeaderQueue';
import TaskTypes from './pages/TaskTypes';
import TaskTypeDetail from './pages/TaskTypeDetail';
import DevPanel from './pages/DevPanel';
import TaskPage from './pages/TaskPage';

export interface Me {
  user: { user_id: string; name: string; team: string; role: string };
  dev: boolean;
  now: number;
}

interface Session {
  me: Me | null;
  refreshKey: number;
  bump: () => void;
}

const SessionCtx = createContext<Session>({ me: null, refreshKey: 0, bump: () => {} });
export const useSession = () => useContext(SessionCtx);

const roleText: Record<string, string> = { rm: 'RM', leader: 'Team Leader', config: '配置人员', admin: '管理员' };

function TopBar({ me, onSwitch, bump }: { me: Me | null; onSwitch: (u: string) => void; bump: () => void }) {
  const [members, setMembers] = useState<any[]>([]);
  const [now, setNow] = useState<number>(0);
  const run = useRun();
  useEffect(() => {
    get('/api/members').then(setMembers).catch(() => {});
  }, [me?.user.user_id]);
  useEffect(() => {
    if (me?.dev) get('/api/dev/clock').then((c) => setNow(c.now)).catch(() => {});
  }, [me]);
  const role = me?.user.role;
  const advance = async (d: string) => {
    const r = await run(() => post('/api/dev/clock', { advance: d }));
    if (r) {
      setNow(r.now);
      bump();
    }
  };
  const tick = async () => {
    const r = await run(() => post('/api/dev/tick'));
    if (r) {
      bump();
    }
  };
  return (
    <header className="topbar">
      <span className="brand">Bywork</span>
      <nav className="nav">
        <NavLink to="/my">我的待办</NavLink>
        {(role === 'leader' || role === 'admin') && <NavLink to="/team">团队队列</NavLink>}
        {(role === 'config' || role === 'admin' || role === 'leader') && <NavLink to="/types">任务类型</NavLink>}
        {me?.dev && <NavLink to="/dev">演示台</NavLink>}
      </nav>
      <span className="spacer" />
      {me?.dev && (
        <span className="devbar" title="开发模式：推进系统时间以观察 T+3、48h、7 天等行为">
          <span>系统时间</span>
          <span className="clock">{fmtTime(now)}</span>
          {['1h', '1d', '3d'].map((d) => (
            <button key={d} className="small" onClick={() => advance(d)}>
              +{d}
            </button>
          ))}
          <button className="small" onClick={tick} title="执行一次扫描 / timer / outbox 心跳">
            心跳
          </button>
        </span>
      )}
      <select value={me?.user.user_id || ''} onChange={(e) => onSwitch(e.target.value)} title="切换身份（开发模式）">
        {members.map((m) => (
          <option key={m.user_id} value={m.user_id}>
            {m.name} · {roleText[m.role] || m.role}
            {m.active ? '' : '（已停用）'}
          </option>
        ))}
      </select>
    </header>
  );
}

function Shell() {
  const [me, setMe] = useState<Me | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [err, setErr] = useState('');
  const bump = useCallback(() => setRefreshKey((k) => k + 1), []);
  const load = useCallback(() => {
    get<Me>('/api/me')
      .then((m) => {
        setMe(m);
        setErr('');
      })
      .catch((e) => setErr(e.message));
  }, []);
  useEffect(load, [load]);
  const onSwitch = (u: string) => {
    setCurrentUser(u);
    load();
    bump();
  };
  const role = me?.user.role;
  const home = role === 'leader' ? '/team' : role === 'config' || role === 'admin' ? '/types' : '/my';
  return (
    <SessionCtx.Provider value={{ me, refreshKey, bump }}>
      <div className="app">
        <TopBar me={me} onSwitch={onSwitch} bump={bump} />
        {err && (
          <div className="main">
            <div className="panel">
              无法登录为 <code>{currentUser()}</code>：{err}
            </div>
          </div>
        )}
        {me && (
          <Routes>
            <Route path="/" element={<Navigate to={home} replace />} />
            <Route path="/my" element={<MyTasks />} />
            <Route path="/tasks/:id" element={<TaskPage />} />
            <Route path="/team" element={<LeaderQueue />} />
            <Route path="/types" element={<TaskTypes />} />
            <Route path="/types/:key" element={<TaskTypeDetail />} />
            <Route path="/types/:key/:tab" element={<TaskTypeDetail />} />
            <Route path="/dev" element={<DevPanel />} />
            <Route path="*" element={<Navigate to={home} replace />} />
          </Routes>
        )}
      </div>
    </SessionCtx.Provider>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <Shell />
      </ToastProvider>
    </BrowserRouter>
  );
}
