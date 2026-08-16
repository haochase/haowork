import type {
  Actor,
  AttributeRequest,
  ChangeQueueResponse,
  EvidenceCandidateRequest,
  Event as HistoryEvent,
  Evidence,
  FinishRunRequest,
  PlanRequest,
  PlanResponse,
  ProjectResponse,
  Run,
  StartRunRequest,
  StateSnapshot,
  TeamConflict,
  TeamConflictResolution,
  TeamLease,
  TeamQueueEntry,
  TeamStatus,
  TeamSyncReport,
  VerifyRequest,
  MissionEnvelope, AgentTopology, SkillDefinition, TraceEnvelope, ApprovalRequest, TransferPreview,
} from "./types";

const API_PREFIX = "/api/v1";
const PROJECT_PATH = `${API_PREFIX}/project`;
const EVENTS_PATH = `${API_PREFIX}/events`;
const REQUIREMENTS_PATH = `${API_PREFIX}/requirements`;
const CHANGES_PATH = `${API_PREFIX}/changes`;

export interface ApiErrorPayload {
  code?: string;
  message?: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, payload: ApiErrorPayload = {}) {
    super(payload.message || `Local API returned ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = payload.code || "unknown";
  }
}

export interface ApiClient {
  getProject(): Promise<ProjectResponse>;
  createRequirement(input: PlanRequest): Promise<PlanResponse>;
  approveRequirement(id: string, actor: Actor): Promise<void>;
  scanChanges(actor: Actor): Promise<ChangeQueueResponse>;
  attributeChange(path: string, input: AttributeRequest): Promise<void>;
  subscribe(onChange: (snapshot?: StateSnapshot) => void): () => void;
  getHistory(aggregateId?: string): Promise<HistoryEvent[]>;
  startRun(taskId: string, input: StartRunRequest): Promise<Run>;
  finishRun(runId: string, input: FinishRunRequest): Promise<void>;
  verifyTask(taskId: string, input: VerifyRequest): Promise<Evidence>;
  recordEvidenceCandidate(taskId: string, input: EvidenceCandidateRequest): Promise<Evidence>;
  verifyEvidence(evidenceId: string, actor: Actor): Promise<Evidence>;
  completeTask(taskId: string, actor: Actor): Promise<void>;
  getTeamStatus(): Promise<TeamStatus>;
  getTeamLeases(): Promise<TeamLease[]>;
  getTeamQueue(): Promise<TeamQueueEntry[]>;
  getTeamConflicts(): Promise<TeamConflict[]>;
  syncTeam(): Promise<TeamSyncReport>;
  resolveTeamConflict(id: string, input: TeamConflictResolution): Promise<unknown>;
  getMissions?(): Promise<MissionEnvelope[]>;
  issueMission?(input: Record<string, unknown>): Promise<MissionEnvelope>;
  getAgentTopology?(): Promise<AgentTopology>;
  getSkills?(): Promise<SkillDefinition[]>;
  getTraces?(missionId?: string, after?: string): Promise<{ traces: TraceEnvelope[]; next?: string }>;
  getApprovals?(): Promise<ApprovalRequest[]>;
  decideApproval?(id: string, input: { payload_sha256: string; decision: string; reason?: string; actor: Actor }): Promise<ApprovalRequest>;
  previewTransfer?(archive: Uint8Array | string): Promise<TransferPreview>;
  applyTransfer?(previewHash: string, actor: Actor, confirmed: boolean): Promise<void>;
  rebindAgent?(id: string, binding: Record<string, unknown>, actor: Actor, confirmed: boolean): Promise<Record<string, unknown>>;
}

export interface EventSourceLike {
  onopen: ((event?: globalThis.Event) => void) | null;
  onerror: ((event?: globalThis.Event) => void) | null;
  addEventListener(type: string, listener: (event?: globalThis.Event) => void): void;
  close(): void;
}

export type EventSourceFactory = (
  url: string,
  init?: { withCredentials?: boolean },
) => EventSourceLike;

interface ClientOptions {
  fetcher?: typeof fetch;
  eventSourceFactory?: EventSourceFactory;
  onConnectionChange?: (state: "connected" | "disconnected") => void;
}

interface GoPlanInput {
  Title: string;
  Constraints: string[];
  Tasks: Array<{ Title: string; AcceptanceCriteria: string[] }>;
  Actor: Actor;
}

function toGoPlanInput(input: PlanRequest): GoPlanInput {
  return {
    Title: input.title,
    Constraints: input.constraints,
    Tasks: input.tasks.map((task) => ({ Title: task.title, AcceptanceCriteria: task.acceptance_criteria })),
    Actor: input.actor,
  };
}

function defaultEventSourceFactory(url: string, init?: { withCredentials?: boolean }): EventSourceLike {
  return new EventSource(url, init) as unknown as EventSourceLike;
}

async function decodeError(response: Response): Promise<ApiError> {
  let payload: ApiErrorPayload = {};
  try {
    payload = (await response.json()) as ApiErrorPayload;
  } catch {
    // A non-JSON response is still represented as an ApiError with its status.
  }
  return new ApiError(response.status, payload);
}

async function request<T>(
  fetcher: typeof fetch,
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const response = await fetcher(path, {
    ...init,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
  });
  if (!response.ok) {
    throw await decodeError(response);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

function jsonBody(value: unknown): RequestInit {
  return { method: "POST", body: JSON.stringify(value) };
}

export function createApiClient(options: ClientOptions = {}): ApiClient {
  const fetcher = options.fetcher ?? fetch;
  const eventSourceFactory = options.eventSourceFactory ?? defaultEventSourceFactory;

  const client: ApiClient = {
    getProject: () => request<ProjectResponse>(fetcher, PROJECT_PATH),

    createRequirement: (input) =>
      request<PlanResponse>(fetcher, REQUIREMENTS_PATH, jsonBody(toGoPlanInput(input))),

    approveRequirement: (id, actor) =>
      request<void>(
        fetcher,
        `${REQUIREMENTS_PATH}/${encodeURIComponent(id)}/approve`,
        jsonBody({ actor }),
      ),

    scanChanges: (actor) =>
      request<ChangeQueueResponse>(fetcher, `${CHANGES_PATH}/scan`, jsonBody({ actor })),

    attributeChange: (path, input) =>
      request<void>(
        fetcher,
        `${CHANGES_PATH}/${encodeURIComponent(path)}/attribute`,
        jsonBody({ sha256: input.sha256, task_id: input.task_id, note: input.note ?? "", actor: input.actor }),
      ),

    getHistory: (aggregateId = "") => {
      const query = aggregateId ? `?aggregate_id=${encodeURIComponent(aggregateId)}` : "";
      return request<{ events: HistoryEvent[] }>(fetcher, `${API_PREFIX}/history${query}`).then(
        (response) => response.events,
      );
    },

    startRun: (taskId, input) =>
      request<Run>(fetcher, `${API_PREFIX}/tasks/${encodeURIComponent(taskId)}/runs`, jsonBody(input)),

    finishRun: (runId, input) =>
      request<void>(fetcher, `${API_PREFIX}/runs/${encodeURIComponent(runId)}/finish`, jsonBody(input)),

    verifyTask: (taskId, input) =>
      request<Evidence>(
        fetcher,
        `${API_PREFIX}/tasks/${encodeURIComponent(taskId)}/evidence`,
        jsonBody(input),
      ),

    recordEvidenceCandidate: (taskId, input) =>
      request<Evidence>(
        fetcher,
        `${API_PREFIX}/tasks/${encodeURIComponent(taskId)}/evidence/candidates`,
        jsonBody(input),
      ),

    verifyEvidence: (evidenceId, actor) =>
      request<Evidence>(
        fetcher,
        `${API_PREFIX}/evidence/${encodeURIComponent(evidenceId)}/verify`,
        jsonBody({ actor }),
      ),

    completeTask: (taskId, actor) =>
      request<void>(fetcher, `${API_PREFIX}/tasks/${encodeURIComponent(taskId)}/complete`, jsonBody({ actor })),

    getTeamStatus: () => request<TeamStatus>(fetcher, `${API_PREFIX}/team/status`),
    getTeamLeases: () => request<{ leases: TeamLease[] }>(fetcher, `${API_PREFIX}/team/leases`).then((response) => response.leases),
    getTeamQueue: () => request<{ queue: TeamQueueEntry[] }>(fetcher, `${API_PREFIX}/team/queue`).then((response) => response.queue),
    getTeamConflicts: () => request<{ conflicts: TeamConflict[] }>(fetcher, `${API_PREFIX}/team/conflicts`).then((response) => response.conflicts),
    syncTeam: () => request<TeamSyncReport>(fetcher, `${API_PREFIX}/team/sync`, jsonBody(undefined)),
    resolveTeamConflict: (id, input) =>
      request<unknown>(fetcher, `${API_PREFIX}/team/conflicts/${encodeURIComponent(id)}/resolve`, jsonBody({
        action: input.action,
        ...(input.replacement !== undefined ? { replacement: input.replacement } : {}),
        ...(input.confirmed !== undefined ? { confirmed: input.confirmed } : {}),
      })),

    getMissions: () => request<{ missions: MissionEnvelope[] }>(fetcher, `${API_PREFIX}/missions`).then((response) => response.missions),
    issueMission: (input) => request<MissionEnvelope>(fetcher, `${API_PREFIX}/missions`, jsonBody(input)),
    getAgentTopology: () => request<AgentTopology>(fetcher, `${API_PREFIX}/agentteams/topology`),
    getSkills: () => request<{ skills: SkillDefinition[] }>(fetcher, `${API_PREFIX}/skills`).then((response) => response.skills),
    getTraces: (missionId = "", after = "") => { const query = new URLSearchParams(); if (missionId) query.set("mission_id", missionId); if (after) query.set("after", after); return request<{ traces: TraceEnvelope[]; next?: string }>(fetcher, `${API_PREFIX}/traces${query.toString() ? `?${query.toString()}` : ""}`); },
    getApprovals: () => request<{ approvals: ApprovalRequest[] }>(fetcher, `${API_PREFIX}/approvals`).then((response) => response.approvals),
    decideApproval: (id, input) => request<ApprovalRequest>(fetcher, `${API_PREFIX}/approvals/${encodeURIComponent(id)}/decide`, jsonBody(input)),
    previewTransfer: (archive) => { const value = typeof archive === "string" ? archive : btoa(String.fromCharCode(...archive)); return request<TransferPreview>(fetcher, `${API_PREFIX}/transfers/preview`, jsonBody({ archive: value })); },
    applyTransfer: (previewHash, actor, confirmed) => request<void>(fetcher, `${API_PREFIX}/transfers/${encodeURIComponent(previewHash)}/apply`, jsonBody({ preview_hash: previewHash, actor, confirmed })),
    rebindAgent: (id, binding, actor, confirmed) => request<Record<string, unknown>>(fetcher, `${API_PREFIX}/agents/${encodeURIComponent(id)}/rebind`, jsonBody({ binding, actor, confirmed })),

    subscribe: (onChange) => {
      let closed = false;
      let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
      let delay = 1_000;
      let source: EventSourceLike | undefined;

      const connect = () => {
        if (closed) return;
        source = eventSourceFactory(EVENTS_PATH, { withCredentials: true });
        source.onopen = () => {
          delay = 1_000;
          options.onConnectionChange?.("connected");
        };
        source.onerror = () => {
          options.onConnectionChange?.("disconnected");
          source?.close();
          if (closed) return;
          reconnectTimer = setTimeout(connect, delay);
          delay = Math.min(delay * 2, 30_000);
        };
        const notifySnapshot = (event?: globalThis.Event) => {
          const data = (event as MessageEventLike | undefined)?.data;
          if (typeof data !== "string") {
            onChange();
            return;
          }
          try {
            onChange(JSON.parse(data) as StateSnapshot);
          } catch {
            onChange();
          }
        };
        source.addEventListener("snapshot", notifySnapshot);
        source.addEventListener("state.changed", notifySnapshot);
      };

      connect();
      return () => {
        closed = true;
        if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
        source?.close();
      };
    },
  };

  return client;
}

interface MessageEventLike {
  data?: unknown;
}

interface BootstrapOptions {
  fetcher?: typeof fetch;
  location?: Partial<Pick<Location, "hash" | "pathname" | "search">>;
  replaceState?: (data: unknown, unused: string, url?: string | URL | null) => void;
}

export async function bootstrapSession(options: BootstrapOptions = {}): Promise<boolean> {
  const location = options.location ?? (typeof globalThis.location === "undefined" ? undefined : globalThis.location);
  if (!location) return false;
  const hash = (location.hash ?? "").replace(/^#/, "");
  const token = new URLSearchParams(hash).get("bootstrap");
  if (!token) return false;

  const fetcher = options.fetcher ?? fetch;
  const response = await fetcher(`${API_PREFIX}/session`, {
    method: "POST",
    credentials: "include",
    headers: { "X-Haowork-Bootstrap": token },
  });
  if (!response.ok) throw await decodeError(response);

  const replaceState =
    options.replaceState ??
    (typeof globalThis.history === "undefined" ? undefined : globalThis.history.replaceState.bind(globalThis.history));
  if (replaceState) {
    const cleanUrl = `${location.pathname ?? ""}${location.search ?? ""}`;
    replaceState(null, "", cleanUrl);
  }
  return true;
}

export const apiPaths = {
  project: PROJECT_PATH,
  requirements: REQUIREMENTS_PATH,
  events: EVENTS_PATH,
  changes: CHANGES_PATH,
} as const;
