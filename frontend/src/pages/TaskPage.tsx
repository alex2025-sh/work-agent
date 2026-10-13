import { useParams } from 'react-router-dom';
import TaskDetail from '../components/TaskDetail';

/** Deep link target for "在 Bywork 中查看" from Lark cards. */
export default function TaskPage() {
  const { id } = useParams();
  return (
    <div className="main">
      <div style={{ maxWidth: 1000, margin: '0 auto' }}>
        <TaskDetail id={Number(id)} />
      </div>
    </div>
  );
}
