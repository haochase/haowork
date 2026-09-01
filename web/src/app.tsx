import { useCallback, useEffect, useMemo, useState } from "react";
import { bootstrapSession, createApiClient, type ApiClient } from "./api/client";
import type { Actor, AgentTopology, ApprovalRequest, GitHubSCMStatus, MissionEnvelope, ProjectResponse, SCMStatus, SkillDefinition, StateSnapshot, Task, TeamConflict, TeamLease, TeamQueueEntry, TeamStatus as TeamStatusDTO, TransferPreview } from "./api/types";
import { ChangeQueue } from "./components/change-queue";
import { ContextPanel } from "./components/context-panel";
import { EvidenceDesk } from "./components/evidence-desk";
import { HistoryReader } from "./components/history-reader";
import { ProjectOverview } from "./components/project-overview";
import { ProjectTree } from "./components/project-tree";
import { ReviewDesk } from "./components/review-desk";
import { RunTimeline } from "./components/run-timeline";
import { TaskConsole } from "./components/task-console";
import { ConflictDesk } from "./components/conflict-desk";
import { LeasePanel } from "./components/lease-panel";
import { SyncQueue } from "./components/sync-queue";
import { TeamStatus } from "./components/team-status";
import { MissionConsole } from "./components/mission-console";
import { AgentTopology as AgentTopologyPanel } from "./components/agent-topology";
import { WorkGraph } from "./components/work-graph";
import { SkillActivity } from "./components/skill-activity";
import { ApprovalInbox } from "./components/approval-inbox";
import { MigrationCenter } from "./components/migration-center";
import { SCMProvenance } from "./components/scm-provenance";
import { SCMRemote as SCMRemotePanel } from "./components/scm-remote";
import "./styles.css";

export interface AppProps {
  client?: ApiClient;
  initialState?: ProjectResponse;
  readOnly?: boolean;
}

export interface TeamWorkbenchState {
  status?: TeamStatusDTO;
  leases: TeamLease[];
  queue: TeamQueueEntry[];
  conflicts: TeamConflict[];
}

export function teamStateFromSnapshot(snapshot: StateSnapshot, current: TeamWorkbenchState): TeamWorkbenchState {
  const team = snapshot.state.team;
  if (!team) return current;
  const withQueue = team as TeamStatusDTO & { queue?: TeamQueueEntry[] };
  return {
    status: team,
    leases: team.active_leases,
    queue: withQueue.queue ?? current.queue,
    conflicts: team.open_conflicts,
  };
}

export function shouldBootstrapClient(providedClient: ApiClient | undefined) {
  return !providedClient;
}

const emptyState: ProjectResponse = {
  project_id: "",
  workspace_digest: "",
  goal: { version: 0, statement: "", invariants: [], completion_criteria: [] },
  requirements: {}, tasks: {}, runs: {}, evidence: {}, contexts: {}, steps: {}, checkpoints: {}, executor_events: {}, changes: {}, attributions: {},
};

export function App({ client: providedClient, initialState, readOnly = false }: AppProps) {
  const [state, setState] = useState<ProjectResponse>(initialState ?? emptyState);
  const [eventCount, setEventCount] = useState<number>();
  const [selectedTaskId, setSelectedTaskId] = useState("");
  const [connection, setConnection] = useState<"connected" | "disconnected" | "connecting">("connecting");
  const [error, setError] = useState("");
  const [missions, setMissions] = useState<MissionEnvelope[]>([]);
  const [topology, setTopology] = useState<AgentTopology>();
  const [skills, setSkills] = useState<SkillDefinition[]>([]);
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([]);
  const [transferPreview, setTransferPreview] = useState<TransferPreview>();
  const [scmStatus, setSCMStatus] = useState<SCMStatus>();
  const [githubSCMStatus, setGitHubSCMStatus] = useState<GitHubSCMStatus>();
  const [teamState, setTeamState] = useState<TeamWorkbenchState>({ status: initialState?.team, leases: initialState?.team?.active_leases ?? [], queue: [], conflicts: initialState?.team?.open_conflicts ?? [] });
  const defaultClient = useMemo(() => createApiClient({ onConnectionChange: setConnection }), []);
  const client = providedClient ?? defaultClient;

  const refreshTeam = useCallback(async () => {
    try {
      const status = await client.getTeamStatus();
      const [leases, queue, conflicts] = await Promise.all([
        client.getTeamLeases(),
        client.getTeamQueue(),
        client.getTeamConflicts(),
      ]);
      setTeamState({ status, leases, queue, conflicts });
    } catch {
      // Personal projects return team_unavailable; keep the four panels absent.
      setTeamState({ leases: [], queue: [], conflicts: [] });
    }
  }, [client]);

  const refresh = useCallback(async () => {
    try {
      const next = await client.getProject();
      setState(next);
      setSelectedTaskId((current) => current && next.tasks[current] ? current : Object.keys(next.tasks)[0] ?? "");
      void refreshTeam();
      if (client.getMissions) void client.getMissions().then(setMissions).catch(() => undefined);
      if (client.getAgentTopology) void client.getAgentTopology().then(setTopology).catch(() => undefined);
      if (client.getSkills) void client.getSkills().then(setSkills).catch(() => undefined);
      if (client.getApprovals) void client.getApprovals().then(setApprovals).catch(() => undefined);
      if (client.getSCMStatus) void client.getSCMStatus().then(setSCMStatus).catch(() => setSCMStatus(undefined));
      if (client.getGitHubSCMStatus) void client.getGitHubSCMStatus().then(setGitHubSCMStatus).catch(() => setGitHubSCMStatus(undefined));
      setError("");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "无法读取项目状态"); }
  }, [client, refreshTeam]);

  useEffect(() => {
    const initialize = async () => {
      try {
        if (shouldBootstrapClient(providedClient)) await bootstrapSession();
        await refresh();
      } catch (cause) { setError(cause instanceof Error ? cause.message : "会话交换失败"); }
    };
    void initialize();
    return client.subscribe((snapshot) => {
      if (snapshot) {
        setEventCount(snapshot.event_count);
        setState({ ...snapshot.state, workspace_digest: snapshot.workspace_digest ?? snapshot.state.workspace_digest ?? "" });
        setTeamState((current) => teamStateFromSnapshot(snapshot, current));
      }
      void refresh();
    });
  }, [client, refresh]);

  const selectedTask: Task | undefined = selectedTaskId ? state.tasks[selectedTaskId] : undefined;
  const selectedRun = selectedTask?.last_run_id ? state.runs[selectedTask.last_run_id] : undefined;
  const selectedContext = selectedRun?.context_id ? state.contexts[selectedRun.context_id] : undefined;
  const selectedEvidence = selectedTask ? state.evidence[selectedTask.id] ?? [] : [];
  const selectedSteps = selectedRun ? Object.values(state.steps).filter((step) => step.run_id === selectedRun.id) : [];
  const selectedCheckpoints = selectedRun ? Object.values(state.checkpoints).filter((checkpoint) => checkpoint.run_id === selectedRun.id) : [];
  const selectedExecutorEvents = selectedRun ? Object.values(state.executor_events).filter((event) => event.run_id === selectedRun.id) : [];
  const tasks = Object.values(state.tasks);
  const changes = Object.values(state.changes);
  const workspaceDigest = state.workspace_digest ?? "";
  const onChanged = () => { void refresh(); };
  const teamStatus = teamState.status;
  const owner: Actor = { id: "workbench-owner", kind: "human", role: "owner" };
  const issueMission = async () => {
    if (!client.issueMission || !selectedTask || !selectedContext) { setError("Mission requires a selected approved task and context"); return; }
    const assignments = Object.fromEntries((topology?.agents ?? []).filter((agent) => agent.agent_function === "build" || agent.agent_function === "verify").map((agent) => [agent.agent_function, agent.id]));
    if (!assignments.build || !assignments.verify || skills.length === 0) { setError("Mission requires build/verify agents and canonical skills"); return; }
    try { const mission = await client.issueMission({ task_ids: [selectedTask.id], context_id: selectedContext.id, completion_criteria: selectedTask.acceptance_criteria, allowed_scopes: selectedContext.allowed_paths ?? ["project"], skills: skills.map((skill) => ({ name: skill.name, version: skill.version })), assignments, risk_level: "L1", environment_id: "local", policy_version: "p0-05", actor: owner }); setMissions((current) => [...current, mission]); } catch (cause) { setError(cause instanceof Error ? cause.message : "Mission issue failed"); }
  };
  const decideApproval = async (id: string) => { const approval = approvals.find((item) => item.id === id); if (!approval || !client.decideApproval) return; try { const decided = await client.decideApproval(id, { payload_sha256: approval.payload_sha256, decision: "approved", reason: "Workbench review", actor: owner }); setApprovals((current) => current.map((item) => item.id === id ? decided : item)); } catch (cause) { setError(cause instanceof Error ? cause.message : "Approval decision failed"); } };
  const previewTransfer = async (archive: Uint8Array) => { if (!client.previewTransfer) return; try { setTransferPreview(await client.previewTransfer(archive)); } catch (cause) { setError(cause instanceof Error ? cause.message : "Transfer preview failed"); } };
  const applyTransfer = async () => { if (!transferPreview || !client.applyTransfer) return; try { await client.applyTransfer(transferPreview.preview_hash, owner, true); } catch (cause) { setError(cause instanceof Error ? cause.message : "Transfer apply failed"); } };
  const registerSCM = async () => { if (!client.registerSCM) return; try { await client.registerSCM(owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "SCM repository registration failed"); } };
  const confirmSCMBinding = async (id: string) => { if (!client.confirmSCMBinding) return; try { await client.confirmSCMBinding(id, owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "SCM binding confirmation failed"); } };
  const rejectSCMBinding = async (id: string, reason: string) => { if (!client.rejectSCMBinding) return; try { await client.rejectSCMBinding(id, reason, owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "SCM binding rejection failed"); } };
  const verifySCMHistory = async (repositoryId: string, refs: string[]) => { if (!client.verifySCMHistory) return; try { await client.verifySCMHistory(repositoryId, refs, owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "SCM history verification failed"); } };
  const connectGitHubSCM = async () => { if (!client.connectGitHubSCM) return; try { await client.connectGitHubSCM(owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "GitHub connection failed"); } };
  const syncGitHubSCM = async () => { if (!client.syncGitHubSCM) return; try { await client.syncGitHubSCM(owner); await refresh(); } catch (cause) { setError(cause instanceof Error ? cause.message : "GitHub sync failed"); } };

  return (
    <main className="workbench-shell">
      <ProjectOverview state={state} eventCount={eventCount} connection={connection} />
      <div className="team-overview"><TeamStatus status={teamStatus} connection={connection} /></div>
      <div className="workbench-grid">
        <ProjectTree state={state} selectedTaskId={selectedTaskId} onSelectTask={(task) => setSelectedTaskId(task.id)} />
        <div className="workspace-column">
          <TaskConsole client={client} task={selectedTask} run={selectedRun} context={selectedContext} evidence={selectedEvidence} changes={changes} workspaceDigest={workspaceDigest} onChanged={onChanged} />
          {teamStatus ? <LeasePanel leases={teamState.leases} /> : null}
          <ContextPanel context={selectedContext} />
          <RunTimeline run={selectedRun} steps={selectedSteps} checkpoints={selectedCheckpoints} executorEvents={selectedExecutorEvents} connection={connection} />
          <EvidenceDesk task={selectedTask} run={selectedRun} context={selectedContext} evidence={selectedEvidence} workspaceDigest={workspaceDigest} onComplete={async () => { if (selectedTask) { await client.completeTask(selectedTask.id, { id: "owner", kind: "human", role: "owner" }); onChanged(); } }} />
          <ReviewDesk client={client} requirements={Object.values(state.requirements)} onChanged={onChanged} />
          {teamStatus ? <ConflictDesk conflicts={teamState.conflicts} role={teamStatus.principal.actor.role} onResolve={async (id, input) => { await client.resolveTeamConflict(id, input); await refreshTeam(); }} /> : null}
          <HistoryReader client={client} aggregateId={selectedTask?.id} />
          <MissionConsole missions={missions} onIssue={() => { void issueMission(); }} />
          <AgentTopologyPanel topology={topology} />
          <WorkGraph state={state} />
          <SkillActivity skills={skills} />
          <ApprovalInbox approvals={approvals} onDecide={(id) => { void decideApproval(id); }} />
          <MigrationCenter preview={transferPreview} onPreview={(archive) => { void previewTransfer(archive); }} onApply={() => { void applyTransfer(); }} />
          <SCMProvenance status={scmStatus} readOnly={readOnly} onRegister={registerSCM} onConfirm={confirmSCMBinding} onReject={rejectSCMBinding} onVerifyHistory={verifySCMHistory} />
          <SCMRemotePanel status={githubSCMStatus} readOnly={readOnly} onConnect={connectGitHubSCM} onSync={syncGitHubSCM} />
        </div>
        <aside className="action-column"><ChangeQueue client={client} changes={changes} tasks={tasks} onChanged={onChanged} />{teamStatus ? <SyncQueue queue={teamState.queue} onSync={async () => { const report = await client.syncTeam(); await refreshTeam(); return report; }} /> : null}</aside>
      </div>
      {error ? <div className="global-error" role="alert" aria-live="polite">{error}</div> : null}
      <span className="sr-only" aria-live="polite">{eventCount ? `已同步 ${eventCount} 个事件` : ""}</span>
    </main>
  );
}

export { emptyState };
