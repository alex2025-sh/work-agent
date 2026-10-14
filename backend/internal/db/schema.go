package db

// schemaTemplate is shared by MySQL (production) and SQLite (local/dev/test).
// {{PK}} and {{TAIL}} are replaced per dialect. Every column is NOT NULL with a
// default so rows scan straight into plain Go structs. Times are unix millis
// (UTC); configuration and snapshots are JSON stored in MEDIUMTEXT.
const schemaTemplate = `
CREATE TABLE IF NOT EXISTS bw_task_type (
  id {{PK}},
  type_key VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL DEFAULT '',
  description VARCHAR(512) NOT NULL DEFAULT '',
  team VARCHAR(64) NOT NULL DEFAULT '',
  visibility VARCHAR(64) NOT NULL DEFAULT 'team',
  object_type VARCHAR(64) NOT NULL DEFAULT '',
  dedupe_fields VARCHAR(512) NOT NULL DEFAULT '[]',
  round_field VARCHAR(128) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL DEFAULT 'draft',
  creator VARCHAR(64) NOT NULL DEFAULT '',
  active_config_version INT NOT NULL DEFAULT 0,
  active_flow_version INT NOT NULL DEFAULT 0,
  shadow_started_at BIGINT NOT NULL DEFAULT 0,
  enabled_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (type_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_task_config (
  id {{PK}},
  type_key VARCHAR(64) NOT NULL,
  version INT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'draft',
  base_version INT NOT NULL DEFAULT 0,
  revision INT NOT NULL DEFAULT 0,
  spec MEDIUMTEXT NOT NULL,
  note VARCHAR(512) NOT NULL DEFAULT '',
  author VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  published_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (type_key, version)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_schedule (
  id {{PK}},
  type_key VARCHAR(64) NOT NULL,
  config_version INT NOT NULL DEFAULT 0,
  cron VARCHAR(64) NOT NULL DEFAULT '',
  timezone VARCHAR(64) NOT NULL DEFAULT 'UTC',
  status VARCHAR(16) NOT NULL DEFAULT 'paused',
  pause_reason VARCHAR(255) NOT NULL DEFAULT '',
  next_fire_at BIGINT NOT NULL DEFAULT 0,
  watermark BIGINT NOT NULL DEFAULT 0,
  checkpoint VARCHAR(1024) NOT NULL DEFAULT '',
  lease_owner VARCHAR(64) NOT NULL DEFAULT '',
  lease_until BIGINT NOT NULL DEFAULT 0,
  last_run_at BIGINT NOT NULL DEFAULT 0,
  last_success_at BIGINT NOT NULL DEFAULT 0,
  consecutive_failures INT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (type_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_schedule_run (
  id {{PK}},
  type_key VARCHAR(64) NOT NULL,
  config_version INT NOT NULL DEFAULT 0,
  source VARCHAR(16) NOT NULL DEFAULT '',
  fired_at BIGINT NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  rows_scanned INT NOT NULL DEFAULT 0,
  tasks_created INT NOT NULL DEFAULT 0,
  result VARCHAR(16) NOT NULL DEFAULT '',
  message VARCHAR(1024) NOT NULL DEFAULT ''
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_task (
  id {{PK}},
  type_key VARCHAR(64) NOT NULL,
  dedupe_key VARCHAR(255) NOT NULL,
  object_key VARCHAR(128) NOT NULL DEFAULT '',
  is_shadow INT NOT NULL DEFAULT 0,
  config_version INT NOT NULL DEFAULT 0,
  flow_version INT NOT NULL DEFAULT 0,
  run_id BIGINT NOT NULL DEFAULT 0,
  title VARCHAR(255) NOT NULL DEFAULT '',
  team VARCHAR(64) NOT NULL DEFAULT '',
  snapshot MEDIUMTEXT NOT NULL,
  origin MEDIUMTEXT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'OPEN',
  close_reason VARCHAR(32) NOT NULL DEFAULT '',
  attention VARCHAR(255) NOT NULL DEFAULT '',
  assignee VARCHAR(64) NOT NULL DEFAULT '',
  next_action_at BIGINT NOT NULL DEFAULT 0,
  next_action_text VARCHAR(255) NOT NULL DEFAULT '',
  lark_message_id VARCHAR(128) NOT NULL DEFAULT '',
  card_rev INT NOT NULL DEFAULT 0,
  delay_count INT NOT NULL DEFAULT 0,
  last_checked_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  closed_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (type_key, dedupe_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_work_item (
  id {{PK}},
  task_id BIGINT NOT NULL,
  run_id BIGINT NOT NULL,
  node_key VARCHAR(64) NOT NULL,
  round INT NOT NULL DEFAULT 1,
  assignee VARCHAR(64) NOT NULL DEFAULT '',
  assign_reason MEDIUMTEXT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'active',
  card MEDIUMTEXT NOT NULL,
  card_rev INT NOT NULL DEFAULT 0,
  buttons MEDIUMTEXT NOT NULL,
  action VARCHAR(64) NOT NULL DEFAULT '',
  action_by VARCHAR(64) NOT NULL DEFAULT '',
  action_note VARCHAR(1024) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  delivered_at BIGINT NOT NULL DEFAULT 0,
  done_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (run_id, node_key, round)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_team_member (
  id {{PK}},
  user_id VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL DEFAULT '',
  team VARCHAR(64) NOT NULL DEFAULT '',
  role VARCHAR(32) NOT NULL DEFAULT 'rm',
  lark_open_id VARCHAR(128) NOT NULL DEFAULT '',
  active INT NOT NULL DEFAULT 1,
  UNIQUE (user_id)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_rm_override (
  id {{PK}},
  uid VARCHAR(64) NOT NULL,
  rm_user VARCHAR(64) NOT NULL,
  approval_no VARCHAR(64) NOT NULL DEFAULT '',
  valid_until BIGINT NOT NULL DEFAULT 0,
  revoked INT NOT NULL DEFAULT 0,
  created_by VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_task_log (
  id {{PK}},
  task_id BIGINT NOT NULL,
  at BIGINT NOT NULL DEFAULT 0,
  kind VARCHAR(32) NOT NULL DEFAULT '',
  actor VARCHAR(64) NOT NULL DEFAULT '',
  text VARCHAR(1024) NOT NULL DEFAULT ''
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_feedback (
  id {{PK}},
  task_id BIGINT NOT NULL,
  type_key VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL DEFAULT '',
  kind VARCHAR(32) NOT NULL DEFAULT '',
  note VARCHAR(1024) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_push_event (
  id {{PK}},
  event_id VARCHAR(191) NOT NULL,
  type_key VARCHAR(64) NOT NULL DEFAULT '',
  received_at BIGINT NOT NULL DEFAULT 0,
  result VARCHAR(255) NOT NULL DEFAULT '',
  UNIQUE (event_id)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_audit (
  id {{PK}},
  at BIGINT NOT NULL DEFAULT 0,
  actor VARCHAR(64) NOT NULL DEFAULT '',
  action VARCHAR(64) NOT NULL DEFAULT '',
  target VARCHAR(128) NOT NULL DEFAULT '',
  detail MEDIUMTEXT NOT NULL
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_definition (
  id {{PK}},
  flow_key VARCHAR(64) NOT NULL,
  version INT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'draft',
  base_version INT NOT NULL DEFAULT 0,
  revision INT NOT NULL DEFAULT 0,
  graph MEDIUMTEXT NOT NULL,
  layout MEDIUMTEXT NOT NULL,
  cards MEDIUMTEXT NOT NULL,
  note VARCHAR(512) NOT NULL DEFAULT '',
  author VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  published_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (flow_key, version)
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_run (
  id {{PK}},
  flow_key VARCHAR(64) NOT NULL,
  flow_version INT NOT NULL DEFAULT 0,
  task_id BIGINT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  vars MEDIUMTEXT NOT NULL,
  result MEDIUMTEXT NOT NULL,
  lease_owner VARCHAR(64) NOT NULL DEFAULT '',
  lease_until BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_token (
  id {{PK}},
  run_id BIGINT NOT NULL,
  node_key VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL DEFAULT 'ready',
  seq INT NOT NULL DEFAULT 0,
  state MEDIUMTEXT NOT NULL,
  wait_reason VARCHAR(255) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_node_exec (
  id {{PK}},
  run_id BIGINT NOT NULL,
  token_id BIGINT NOT NULL,
  node_key VARCHAR(64) NOT NULL,
  seq INT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  input MEDIUMTEXT NOT NULL,
  output MEDIUMTEXT NOT NULL,
  exit_name VARCHAR(64) NOT NULL DEFAULT '',
  error VARCHAR(1024) NOT NULL DEFAULT '',
  started_at BIGINT NOT NULL DEFAULT 0,
  finished_at BIGINT NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_timer (
  id {{PK}},
  run_id BIGINT NOT NULL,
  token_id BIGINT NOT NULL,
  token_seq INT NOT NULL DEFAULT 0,
  node_key VARCHAR(64) NOT NULL DEFAULT '',
  timer_key VARCHAR(128) NOT NULL,
  due_at BIGINT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  lease_owner VARCHAR(64) NOT NULL DEFAULT '',
  lease_until BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (token_id, timer_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS wf_event (
  id {{PK}},
  run_id BIGINT NOT NULL,
  event_key VARCHAR(191) NOT NULL,
  kind VARCHAR(32) NOT NULL DEFAULT '',
  payload MEDIUMTEXT NOT NULL,
  payload_hash VARCHAR(64) NOT NULL DEFAULT '',
  result MEDIUMTEXT NOT NULL,
  created_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (run_id, event_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS outbox (
  id {{PK}},
  event_key VARCHAR(191) NOT NULL,
  channel VARCHAR(32) NOT NULL DEFAULT '',
  kind VARCHAR(32) NOT NULL DEFAULT '',
  task_id BIGINT NOT NULL DEFAULT 0,
  target VARCHAR(128) NOT NULL DEFAULT '',
  payload MEDIUMTEXT NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  attempts INT NOT NULL DEFAULT 0,
  next_retry_at BIGINT NOT NULL DEFAULT 0,
  last_error VARCHAR(1024) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  sent_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (event_key)
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_ai_session (
  id VARCHAR(40) NOT NULL PRIMARY KEY,
  type_key VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  title VARCHAR(255) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL DEFAULT 'idle',
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_ai_message (
  id {{PK}},
  session_id VARCHAR(40) NOT NULL,
  turn INT NOT NULL DEFAULT 0,
  role VARCHAR(16) NOT NULL,
  content MEDIUMTEXT NOT NULL,
  tool_calls MEDIUMTEXT NOT NULL,
  tool_call_id VARCHAR(128) NOT NULL DEFAULT '',
  name VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS bw_ai_proposal (
  id {{PK}},
  session_id VARCHAR(40) NOT NULL,
  type_key VARCHAR(64) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  summary VARCHAR(1024) NOT NULL DEFAULT '',
  base_revision INT NOT NULL DEFAULT 0,
  payload MEDIUMTEXT NOT NULL,
  validation MEDIUMTEXT NOT NULL,
  ok INT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  created_by VARCHAR(64) NOT NULL DEFAULT '',
  decided_by VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  decided_at BIGINT NOT NULL DEFAULT 0
){{TAIL}};

CREATE TABLE IF NOT EXISTS demo_case (
  id {{PK}},
  appeal_id VARCHAR(64) NOT NULL,
  uid VARCHAR(64) NOT NULL DEFAULT '',
  case_type VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT 'open',
  stage VARCHAR(64) NOT NULL DEFAULT '',
  stage_entered_at BIGINT NOT NULL DEFAULT 0,
  maker_review_entry_id VARCHAR(64) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE (appeal_id)
){{TAIL}};

CREATE TABLE IF NOT EXISTS demo_vip (
  id {{PK}},
  uid VARCHAR(64) NOT NULL,
  is_vip INT NOT NULL DEFAULT 0,
  level VARCHAR(32) NOT NULL DEFAULT '',
  source VARCHAR(64) NOT NULL DEFAULT '',
  six_month_profit VARCHAR(64) NOT NULL DEFAULT '',
  rm_user VARCHAR(64) NOT NULL DEFAULT '',
  UNIQUE (uid)
){{TAIL}};
`

// indexes are created once, right after the tables are first created.
var indexes = []string{
	"CREATE INDEX idx_schedule_due ON bw_schedule (status, next_fire_at, lease_until)",
	"CREATE INDEX idx_schedule_run_type ON bw_schedule_run (type_key, id)",
	"CREATE INDEX idx_task_assignee ON bw_task (assignee, status)",
	"CREATE INDEX idx_task_attention ON bw_task (attention)",
	"CREATE INDEX idx_task_object ON bw_task (object_key)",
	"CREATE INDEX idx_task_type_status ON bw_task (type_key, status)",
	"CREATE INDEX idx_work_item_task ON bw_work_item (task_id)",
	"CREATE INDEX idx_work_item_assignee ON bw_work_item (assignee, status)",
	"CREATE INDEX idx_member_team ON bw_team_member (team)",
	"CREATE INDEX idx_override_uid ON bw_rm_override (uid)",
	"CREATE INDEX idx_task_log_task ON bw_task_log (task_id, id)",
	"CREATE INDEX idx_feedback_type ON bw_feedback (type_key)",
	"CREATE INDEX idx_run_status ON wf_run (status, lease_until)",
	"CREATE INDEX idx_token_run ON wf_token (run_id, status)",
	"CREATE INDEX idx_node_exec_run ON wf_node_exec (run_id, id)",
	"CREATE INDEX idx_timer_due ON wf_timer (status, due_at, lease_until)",
	"CREATE INDEX idx_outbox_retry ON outbox (status, next_retry_at)",
	"CREATE INDEX idx_demo_case_stage ON demo_case (stage, stage_entered_at)",
	"CREATE INDEX idx_ai_session_type ON bw_ai_session (type_key, user_id, updated_at)",
	"CREATE INDEX idx_ai_message_session ON bw_ai_message (session_id, id)",
	"CREATE INDEX idx_ai_proposal_session ON bw_ai_proposal (session_id, id)",
}
