import { readFileSync, readdirSync, lstatSync } from 'node:fs';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(process.argv[2] ?? resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const requireValue = (condition, message) => { if (!condition) throw new Error(message); };
try {
  const s = JSON.parse(readFileSync(join(root, 'project/scenario.json'), 'utf8'));
  requireValue(s.fictional === true && s.runtime_status === 'NOT_RUN', 'fictional / runtime boundary');
  requireValue(s.schema_version === '1' && s.version && s.goal && s.requirement?.root_cause?.trim(), 'goal / requirement root cause');
  for (const field of ['tasks', 'decisions', 'scope', 'owners', 'checks']) {
    requireValue(Array.isArray(s[field]) && s[field].length > 0, `nonempty ${field} required`);
  }
  for (const field of ['scope', 'owners', 'checks']) {
    requireValue(s[field].every(value => typeof value === 'string' && value.trim()), `nonempty ${field} values required`);
  }
  requireValue(JSON.stringify(s.platforms.map(p => p.id).sort()) === JSON.stringify(['android', 'ios', 'macos', 'web', 'windows']), 'exact five platforms required');
  const tasks = new Set(s.tasks.map(t => t.id));
  const decisions = new Set(s.decisions.map(d => d.id));
  requireValue(tasks.size === s.tasks.length && decisions.size === s.decisions.length, 'duplicate reference IDs');
  for (const t of s.tasks) requireValue(s.owners.includes(t.owner) && t.done_when && t.checks.length && t.decisions.every(d => decisions.has(d)), 'task reference / completion');
  for (const d of s.decisions) requireValue(s.owners.includes(d.owner) && d.rationale && d.tasks.every(t => tasks.has(t)), 'decision reference');
  requireValue(s.gates.report_can_complete === false && s.gates.merged_pr_can_complete === false && s.gates.human_acceptance_required === true && s.gates.report_result === 'candidate', 'invalid state upgrade');
  for (const p of s.platforms) {
    requireValue(p.adapter === `project/adapters/${p.id}/README.md` && p.point, 'adapter reference');
    readFileSync(join(root, p.adapter), 'utf8');
  }
  const required = ['README.md', 'teamai/README.md', 'project/README.md', 'project/docs/compatibility.md', 'project/docs/decision-history.md', 'evidence/README.md'];
  for (const path of required) readFileSync(join(root, path), 'utf8');
  function scan(directory) {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (entry.name.startsWith('.test-')) continue;
      const path = join(directory, entry.name);
      requireValue(!lstatSync(path).isSymbolicLink(), 'symlink forbidden');
      requireValue(!/^(\.env(?:\..*)?|credentials.*|id_rsa|.*\.(?:pem|key|p12))$/i.test(entry.name), 'sensitive configuration file');
      if (entry.isDirectory()) { scan(path); continue; }
      const content = readFileSync(path, 'utf8');
      requireValue(!/(?:sk-[A-Za-z0-9]{20,}|ghp_[A-Za-z0-9]+|-----BEG[I]N .*PRIVATE KEY|(?:api_key|password|token)\s*[:=]\s*\S+|https?:\/\/(?:10\.|192\.168\.|127\.|172\.(?:1[6-9]|2\d|3[01])\.))/i.test(content), 'sensitive content');
    }
  }
  scan(root);
  console.log('TEAMAI_HAOWORK_DEMO_CONTRACT_OK');
} catch (error) {
  console.error(`DEMO_CONTRACT_INVALID: ${error.message}`);
  process.exitCode = 1;
}
