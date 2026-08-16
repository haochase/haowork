import { useState, type ChangeEvent } from "react";
import type { TransferPreview } from "../api/types";

export function MigrationCenter({ preview, onPreview, onApply }: { preview?: TransferPreview; onPreview?: (archive: Uint8Array) => void; onApply?: () => void }) {
  const [confirmed, setConfirmed] = useState(false);
  const chooseArchive = async (event: ChangeEvent<HTMLInputElement>) => { const file = event.target.files?.[0]; if (file) onPreview?.(new Uint8Array(await file.arrayBuffer())); };
  return <section className="governance-panel" aria-labelledby="migration-center-title"><header><h2 id="migration-center-title">Migration Center</h2><label>Signed archive<input type="file" onChange={(event) => { void chooseArchive(event); }} /></label></header>{preview ? <><code>{preview.preview_hash}</code><span>{preview.rebind_required.length} rebind(s)</span><label><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /> Confirm preview hash and owner approval</label><button type="button" disabled={!confirmed} onClick={onApply}>Apply with approval</button></> : <p>No transfer preview</p>}</section>;
}
