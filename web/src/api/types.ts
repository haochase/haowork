export type Status =
  | "Draft"
  | "Approved"
  | "Running"
  | "Verifying"
  | "Verified"
  | "Completed"
  | "Finished"
  | "Paused"
  | "Failed"
  | "Cancelled";

export type ActorKind = "human" | "agent";
export type ActorRole = "owner" | "lead" | "contributor" | "reviewer" | "agent";

export interface Actor {
  id: string;
  kind: ActorKind;
  role: ActorRole;
}

export interface GoalVersion {
  version: number;
  statement: string;
  invariants: string[];
  completion_criteria: string[];
}

export interface Requirement {
  id: string;
  goal_version: number;
  title: string;
  constraints: string[];
  status: Status;
}

export interface Task {
  id: string;
  requirement_id: string;
  goal_version: number;
  title: string;
  acceptance_criteria: string[];
  status: Status;
  last_run_id?: string;
}

export interface Run {
  id: string;
  task_id: string;
  goal_version: number;
  executor: string;
  actor_id: string;
  context_id?: string;
  context_hash?: string;
  adapter_cursor?: string;
  status: Status;
  result?: string;
}

export interface Evidence {
  id: string;
  task_id: string;
  kind: string;
  uri: string;
  sha256: string;
  run_id?: string;
  context_id?: string;
  goal_version?: number;
  outcome?: string;
  status?: string;
  command?: string;
  baseline?: string;
  source?: string;
  actor?: Actor;
  checks?: EvidenceCheck[];
}

export interface EvidenceCheck {
  name: string;
  status: string;
  detail: string;
}

export interface ContextSource {
  kind: string;
  ref: string;
  digest: string;
  reason: string;
  event_ids?: string[];
  excerpt?: string;
}

export interface ContextSlice {
  id: string;
  task_id: string;
  goal_version: number;
  revision: number;
  summary: string;
  sources?: ContextSource[];
  allowed_paths?: string[];
  denied_paths?: string[];
  supersedes_id?: string;
  slice_hash: string;
  superseded: boolean;
}

export interface ArtifactRef {
  kind: string;
  uri: string;
  sha256: string;
}

export interface Step {
  id: string;
  run_id: string;
  kind: string;
  status: string;
  summary: string;
  artifact_refs?: ArtifactRef[];
}

export interface Checkpoint {
  id: string;
  run_id: string;
  step_id: string;
  context_hash: string;
  workspace_digest: string;
  adapter_cursor: string;
}

export interface ExecutorEvent {
  run_id: string;
  step_id: string;
  kind: string;
  cursor: string;
  summary: string;
  artifacts?: ArtifactRef[];
}

export interface FileChange {
  path: string;
  status: string;
  sha256: string;
  baseline: string;
  attributed: boolean;
}

export interface ChangeAttribution {
  path: string;
  sha256: string;
  task_id: string;
  note?: string;
}

export interface Event {
  sequence: number;
  id: string;
  type: string;
  project_id: string;
  goal_version: number;
  aggregate_type: string;
  aggregate_id: string;
  actor: Actor;
  occurred_at: string;
  payload: unknown;
  previous_hash?: string;
  hash: string;
}

export interface ProjectResponse {
  project_id: string;
  workspace_digest?: string;
  goal: GoalVersion;
  requirements: Record<string, Requirement>;
  tasks: Record<string, Task>;
  runs: Record<string, Run>;
  evidence: Record<string, Evidence[]>;
  contexts: Record<string, ContextSlice>;
  steps: Record<string, Step>;
  checkpoints: Record<string, Checkpoint>;
  executor_events: Record<string, ExecutorEvent>;
  changes: Record<string, FileChange>;
  attributions: Record<string, ChangeAttribution>;
  team?: TeamStatus;
}

export interface TeamPrincipal {
  authenticated_principal: string;
  actor: Actor;
  device_id: string;
  environment_id: string;
  functional_identity: string;
  allowed_skills: string[];
}

export interface TeamLease {
  id: string;
  task_id: string;
  subject_kind: string;
  subject_id: string;
  environment_id: string;
  agent_teams_instance: string;
  context_id: string;
  goal_version: number;
  revision: number;
  allowed_scopes: string[];
  allowed_skills: string[];
  risk_level: string;
  status: string;
  starts_at: string;
  expires_at: string;
}

export interface TeamStatus {
  project_id: string;
  team_seq: number;
  writable: boolean;
  materialized_through: number;
  goal_version: number;
  principal: TeamPrincipal;
  active_leases: TeamLease[];
  open_conflicts: TeamConflict[];
}

export type TeamQueueStatus = "Pending" | "Accepted" | "Rejected" | "Conflict" | "Withdrawn";

export interface TeamQueueEntry {
  batch: { batch_id: string; base_team_seq: number; events: Event[] };
  status: TeamQueueStatus;
  result?: { status?: string; code?: string; message?: string; conflict_id?: string; materialized?: boolean };
  materialized: boolean;
  git_committed: boolean;
  updated_at: string;
}

export interface TeamConflict {
  id: string;
  type: string;
  entity_id: string;
  status: string;
  resolver_id: string;
  resolution: string;
  common_base: number;
  team_version: number;
  local_version: number;
  affected_scope: string[];
  suggested_actions: string[];
  local_events: Event[];
  created_at: string;
  resolved_at: string;
}

export interface TeamSyncReport {
  cursor: number;
  pulled: number;
  accepted: number;
  rejected: number;
  conflicts: number;
  pending: number;
  results: Array<{ status?: string; code?: string; message?: string; conflict_id?: string }>;
}

export interface TeamConflictResolution {
  action: string;
  replacement?: Event[];
  confirmed?: boolean;
}

export interface PlanTaskInput {
  title: string;
  acceptance_criteria: string[];
}

export interface PlanRequest {
  title: string;
  constraints: string[];
  tasks: PlanTaskInput[];
  actor: Actor;
}

export interface PlanResponse {
  requirement: Requirement;
  tasks: Task[];
}

export interface AttributeRequest {
  sha256: string;
  task_id: string;
  note?: string;
  actor: Actor;
}

export interface ChangeQueueResponse {
  changes: FileChange[];
}

export interface StateSnapshot {
  event_count: number;
  project_id: string;
  workspace_digest?: string;
  state: ProjectResponse;
}

export interface StartRunRequest {
  executor: string;
  actor: Actor;
}

export interface FinishRunRequest {
  result: string;
  actor: Actor;
}

export interface VerifyRequest {
  kind: string;
  uri: string;
  sha256: string;
  outcome: string;
  actor: Actor;
}

export interface EvidenceCandidateRequest {
  run_id: string;
  context_id: string;
  kind: string;
  uri: string;
  sha256: string;
  command: string;
  outcome: string;
  actor: Actor;
}

export interface MissionEnvelope { id: string; project_id: string; context_id: string; context_hash: string; lease_id: string; policy_version: string; goal_version: number; governance_task_ids: string[]; completion_criteria: string[]; allowed_scopes: string[]; allowed_skills: Array<{ name: string; version: string }>; role_assignments: Record<string, string>; risk_level: string; environment_id: string; issued_at: string; deadline: string; hash: string; }
export interface AgentTopology { agents: Array<{ id: string; subject_kind: ActorKind; governance_role: ActorRole; agent_function: string; status: string }>; bindings: Record<string, Array<{ logical_actor_id: string; revision: number; environment_id: string; agentteams_instance_id: string; runtime_principal_id: string; human_principal_id?: string; leader_room_id?: string; team_room_id?: string; status: string }>>; }
export interface SkillDefinition { name: string; version: string; risk: string; allowed_functions: string[]; adapter: string; }
export interface TraceEnvelope { sequence: number; id: string; mission_id: string; governance_task_id: string; work_item_id: string; run_id: string; logical_actor_id: string; runtime_binding_revision: number; agent_function?: string; environment_id: string; source_event_type: string; skill_name?: string; skill_version?: string; status: string; error_code?: string; started_at: string; finished_at?: string; }
export interface ApprovalRequest { id: string; subject_type: string; subject_id: string; payload_sha256: string; risk_level: string; requester_id: string; decider_id?: string; status: string; decision_reason?: string; requested_at: string; decided_at?: string; }
export interface TransferPreview { preview_hash: string; manifest: Record<string, unknown>; rebind_required: Array<Record<string, unknown>>; }
