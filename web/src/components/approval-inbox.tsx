import { useState } from "react";
import type { ApprovalRequest } from "../api/types";

export function ApprovalInbox({ approvals, onDecide }: { approvals: ApprovalRequest[]; onDecide?: (id: string) => void }) {
  const [confirmed, setConfirmed] = useState<Record<string, boolean>>({});
  return <section className="governance-panel" aria-labelledby="approval-inbox-title"><h2 id="approval-inbox-title">Approval Inbox</h2><ul>{approvals.map((approval) => <li key={approval.id}><strong>{approval.id}</strong><span>{approval.status} · {approval.risk_level}</span><code>{approval.payload_sha256}</code>{approval.status === "requested" ? <><label><input type="checkbox" checked={confirmed[approval.id] ?? false} onChange={(event) => setConfirmed((current) => ({ ...current, [approval.id]: event.target.checked }))} /> Confirm payload hash</label><button type="button" disabled={!confirmed[approval.id]} onClick={() => onDecide?.(approval.id)}>Review</button></> : null}</li>)}</ul></section>;
}
