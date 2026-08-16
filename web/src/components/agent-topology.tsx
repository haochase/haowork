import type { AgentTopology } from "../api/types";

export function AgentTopology({ topology }: { topology?: AgentTopology }) {
  return <section className="governance-panel" aria-labelledby="agent-topology-title"><h2 id="agent-topology-title">Agent Topology</h2><ul>{topology?.agents.map((agent) => <li key={agent.id}><strong>{agent.id}</strong><span>{agent.agent_function} · {agent.status}</span></li>) ?? <li>No topology available</li>}</ul></section>;
}
