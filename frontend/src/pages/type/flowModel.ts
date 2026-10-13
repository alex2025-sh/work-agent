// Mirrors the engine's exit rules so the editor can draw one handle per exit.

export interface EngineNode {
  key: string;
  type: string;
  name: string;
  config?: any;
}
export interface EngineEdge {
  from: string;
  exit: string;
  to: string;
}
export interface Graph {
  nodes: EngineNode[];
  edges: EngineEdge[];
}
export interface NodeMeta {
  type: string;
  title: string;
  category: string;
  desc: string;
  schema: any;
  color: string;
}
export interface Issue {
  node: string;
  field?: string;
  message: string;
  level: 'error' | 'warning' | 'info';
}

export const TAG: Record<string, string> = {
  start: 'START',
  data: 'DATA',
  human: 'HUMAN_TASK',
  wait: 'WAIT',
  condition: 'CONDITION',
  end: 'END',
};

export function exitsOf(n: EngineNode): string[] {
  const c = n.config || {};
  switch (n.type) {
    case 'start':
    case 'data':
    case 'wait':
      return ['next'];
    case 'end':
      return [];
    case 'condition': {
      const out = (c.branches || []).map((b: any) => b.exit).filter(Boolean);
      if (c.default_exit) out.push(c.default_exit);
      return out;
    }
    case 'human': {
      const out: string[] = [];
      for (const b of c.buttons || []) {
        if (b.exit && (b.kind === 'complete' || b.complete_round) && !out.includes(b.exit)) out.push(b.exit);
      }
      if (c.deadline?.at) {
        const e = c.deadline.exit || 'timeout';
        if (!out.includes(e)) out.push(e);
      }
      return out;
    }
  }
  return [];
}

/** Human-friendly exit label, e.g. "升级并打开 Lark / Closed" for exit done. */
export function exitLabel(n: EngineNode, exit: string): string {
  const c = n.config || {};
  if (n.type === 'human') {
    if (c.deadline?.at && (c.deadline.exit || 'timeout') === exit) return '超时';
    const labels = (c.buttons || []).filter((b: any) => b.exit === exit).map((b: any) => b.label || b.key);
    return labels.length ? labels.join(' / ') : exit;
  }
  if (n.type === 'condition') {
    const b = (c.branches || []).find((x: any) => x.exit === exit);
    if (b?.label) return b.label;
    if (exit === c.default_exit) return exit === 'no' ? '否' : `默认 · ${exit}`;
    return exit === 'yes' ? '是' : exit;
  }
  return exit === 'next' ? '' : exit;
}

const closeText: Record<string, string> = { completed: '处理完成', invalidated: 'Case 已失效', admin_terminated: '管理员终止', flow_failed: '流程执行失败' };

export function summary(n: EngineNode): string {
  const c = n.config || {};
  switch (n.type) {
    case 'start':
      return '接收任务数据';
    case 'data':
      return `${c.capability || '未选择能力'} · 失败${c.on_error === 'retry' ? '重试后挂起' : '挂起并告警'}`;
    case 'human': {
      const ex = exitsOf(n).map((e) => exitLabel(n, e));
      const parts = [`出口：${ex.join(' · ') || '—'}`];
      if (c.deadline?.at) parts.push(`定时 ${c.deadline.at}`);
      if (c.delay?.remind_at) parts.push(`延迟至 ${c.delay.remind_at}`);
      return parts.join('\n');
    }
    case 'wait':
      return `等到 ${c.until || '—'}`;
    case 'condition':
      return (c.branches || []).map((b: any) => `${b.exit}: ${(b.when?.all || []).map((r: any) => r.label || `${r.field} ${r.op} ${r.value ?? ''}`).join(' 且 ')}`).join('\n') || '无分支';
    case 'end':
      return closeText[c.close_reason] || '处理完成';
  }
  return '';
}

export function defaultConfig(type: string, team: string, cardKey: string): any {
  switch (type) {
    case 'data':
      return { capability: 'data.case.get', args: { appeal_id: '{{case.appeal_id}}' }, on_error: 'suspend' };
    case 'human':
      return {
        assignee: { team, rule: 'VIP_UID_TO_RM', uid_field: 'case.uid' },
        card: cardKey,
        round_label: '',
        buttons: [{ key: 'close', label: 'Closed', kind: 'complete', exit: 'done' }],
      };
    case 'wait':
      return { until: 'case.stage_entered_at + 7d' };
    case 'condition':
      return { branches: [{ exit: 'yes', label: '是', when: { all: [] } }], default_exit: 'no' };
    case 'end':
      return { close_reason: 'completed' };
  }
  return {};
}

export const EXTENSIONS = ['AI 处理', '通知', '外部调用 + 回调', '并行 / 汇合', '子流程'];
