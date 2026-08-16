import type { MissionEnvelope } from "../api/types";

export function MissionConsole({ missions, onIssue }: { missions: MissionEnvelope[]; onIssue?: () => void }) {
  return <section className="governance-panel" aria-labelledby="mission-console-title"><header><h2 id="mission-console-title">Mission Console</h2><button type="button" onClick={onIssue}>Issue mission</button></header><ul>{missions.map((mission) => <li key={mission.id}><strong>{mission.id}</strong><span>{mission.risk_level} · {mission.environment_id}</span><code>{mission.hash}</code></li>)}</ul></section>;
}
