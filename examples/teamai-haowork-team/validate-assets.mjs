import { lstatSync, readdirSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, join, relative, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const required = [
  'teamai.yaml',
  'skills/haowork-governed-coding/SKILL.md',
  'skills/haowork-submit-receipt/SKILL.md',
  'rules/haowork-boundaries.md',
  'rules/evidence-first-review.md',
  'agents/haowork-reviewer.yaml',
  'agents/haowork-evidence-checker.yaml',
  'docs/onboarding.md',
  'docs/workflow.md',
  'learnings/README.md',
  'teamwiki/README.md',
  'validate-assets.ps1',
  'validate-assets.sh',
  'validate-assets.mjs',
];
function reject() {
  throw new Error('invalid assets');
}
function textField(value) {
  return typeof value === 'string' && value.trim().length > 0;
}
function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
function read(path) {
  return readFileSync(join(root, path), 'utf8');
}
function frontmatterText(raw) {
  const value = raw.trim();
  if (value.startsWith('"')) {
    try {
      const parsed = JSON.parse(value);
      return textField(parsed) ? parsed : null;
    } catch {
      return null;
    }
  }
  if (value.startsWith("'")) {
    if (!/^'(?:[^']|'')*'$/.test(value)) return null;
    const parsed = value.slice(1, -1).replace(/''/g, "'");
    return textField(parsed) ? parsed : null;
  }
  const plain = value.replace(/(?:^|\s+)#.*$/, '').trim();
  if (!plain || /^(?:null|~|true|false|[-+]?\d+(?:\.\d+)?)$/i.test(plain) || /^[\[\]{}&*!|>@`]/.test(plain)) return null;
  return plain;
}
function scan(directory) {
  for (const item of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, item.name);
    const stat = lstatSync(path);
    // Reject every link, including in-root links, before following any path.
    if (stat.isSymbolicLink()) reject();
    // An independent asset repository owns root Git metadata; never scan it.
    // Nested repositories and linked metadata still fail below/above.
    if (directory === root && item.name === '.git') {
      if (!stat.isDirectory() && !stat.isFile()) reject();
      continue;
    }
    if (item.name === '.git') reject();
    const rel = relative(realpathSync(root), realpathSync(path));
    if (isAbsolute(rel) || rel === '..' || rel.startsWith('../') || rel.startsWith('..\\')) reject();
    if (/(^\.env(?:\.|$)|(?:^|[._-])(?:secrets?|tokens?|keys?|credentials?)(?:[._-]|$)|\.(?:pem|p12|pfx|key)$|^id_(?:rsa|ed25519)$)/i.test(item.name)) reject();
    if (stat.isDirectory()) {
      if (['hooks', '.git', 'node_modules'].includes(item.name)) reject();
      scan(path);
      continue;
    }
    if (!stat.isFile() || stat.size > 1024 * 1024) reject();
    const content = readFileSync(path, 'utf8');
    if (content.includes('\0')) reject();
    const privatePath = /(?:[A-Za-z]:\\(?:Users|haochase|new_job)\\|\/home\/[\w.-]+\/|\\\\[\w.-]+\\[\w$.-]+)/i;
    const credential = /(?:-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|\b(?:gh[pousr]_[A-Za-z0-9]{20,}|AKIA[A-Z0-9]{16}|sk-[A-Za-z0-9]{20,})\b|(?:^|[{,;])\s*["']?(?:api[_-]?key|access[_-]?token|password|secret)["']?\s*[:=]\s*(?:"[^"\r\n]+"|'[^'\r\n]+'|[^\s"'{}\[\],;#]+)|https?:\/\/[^\s/:]+:[^\s/@]+@)/im;
    const privateAddress = /https?:\/\/(?:localhost|127\.\d+\.\d+\.\d+|10\.\d+\.\d+\.\d+|192\.168\.\d+\.\d+|172\.(?:1[6-9]|2\d|3[01])\.\d+\.\d+|[^\s/]+\.(?:internal|local|corp))(?=[:/\s]|$)/i;
    if (privatePath.test(content) || credential.test(content) || privateAddress.test(content)) reject();
  }
}
try {
  scan(root);
  for (const path of required) if (!lstatSync(join(root, path)).isFile()) reject();
  // JSON is a YAML 1.2 subset; accepting only this subset makes parsing portable.
  const config = JSON.parse(read('teamai.yaml'));
  if (!object(config)) reject();
  for (const key of ['team', 'description', 'repo', 'provider']) if (!textField(config[key])) reject();
  if (!['tgit', 'github', 'cnb', 'gitlab', 'gitcode', 'git'].includes(config.provider)) reject();
  const repo = new URL(config.repo);
  // Do not accept URL parser repairs, credentials, local hosts or appended URLs.
  const hostname = repo.hostname.toLowerCase();
  const repoPath = decodeURIComponent(repo.pathname);
  if (!/^https:\/\/[^/@\s\\?#]+\/.+/.test(config.repo)
    || /[\s\\?#]/.test(config.repo)
    || repo.protocol !== 'https:' || repo.username || repo.password
    || !/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/i.test(hostname)
    || /(?:^|\.)(?:localhost|local|internal|corp)$/.test(hostname)
    || !repoPath.replaceAll('/', '').trim() || /[\s\\]|:\/\//.test(repoPath)) reject();
  if (!object(config.sharing) || config.sharing.hooks?.autoApply !== false || config.sharing.mcp?.autoApply !== false || config.sharing.env?.injectShellProfile !== false || config.sharing.webhooks?.enabled !== false || config.usageReport !== false) reject();
  for (const path of required.filter(path => path.startsWith('agents/'))) {
    const agent = JSON.parse(read(path));
    if (!object(agent)) reject();
    for (const key of ['name', 'description', 'instructions']) if (!textField(agent[key])) reject();
  }
  for (const path of required.filter(path => path.endsWith('/SKILL.md'))) {
    const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n/.exec(read(path));
    if (!match) reject();
    for (const key of ['name', 'description']) {
      const fields = match[1].split(/\r?\n/).filter(line => line.startsWith(`${key}:`));
      if (fields.length !== 1) reject();
      const value = frontmatterText(fields[0].slice(key.length + 1));
      if (value === null || (key === 'name' && !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(value))) reject();
    }
  }
  console.log('TEAMAI_HAOWORK_ASSETS_OK');
} catch {
  // Never expose filenames, file contents, parser exceptions or credentials.
  console.error('TEAMAI_HAOWORK_ASSETS_INVALID');
  process.exitCode = 1;
}
