import { lstatSync, readdirSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { isIP } from 'node:net';
import { fileURLToPath } from 'node:url';

const fail = code => { throw new Error(code); };
const codes = new Set(['PATH', 'LINK', 'SIZE', 'BINARY', 'CONTENT', 'INPUT']);
const maximum = 1024 * 1024;

function rejectLinks(target) {
  for (let current = target; ; current = path.dirname(current)) {
    if (lstatSync(current).isSymbolicLink()) fail('LINK');
    if (path.dirname(current) === current) break;
  }
  // Also reject filesystem redirects not surfaced as ordinary symlinks.
  const resolved = realpathSync.native(target);
  if (process.platform === 'win32'
    ? resolved.toLowerCase() !== target.toLowerCase()
    : resolved !== target) fail('LINK');
}

function unsafeURLs(text) {
  for (const match of text.matchAll(/https?:\/\/[^\s'"<>`]+/gi)) {
    let url;
    try { url = new URL(match[0].replace(/[),.;]+$/, '')); } catch { continue; }
    const hostname = url.hostname.replace(/^\[|\]$/g, '');
    if (isIP(hostname) || url.username || url.password || hostname === 'localhost'
      || /\.(?:local|internal)$/.test(hostname)) return true;
  }
  for (const match of text.matchAll(/(?:base[_-]?url|endpoint|service[_-]?url)["']?\s*[:=]\s*["']?(https?:\/\/[^\s'"<>`]+)/gi)) {
    try {
      if (!['example.invalid', 'haowork.112318.xyz'].includes(new URL(match[1]).hostname)) return true;
    } catch { return true; }
  }
  return false;
}

function sensitive(text) {
  return /(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|AKIA[A-Z0-9]{16}|-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----)/.test(text)
    || /\b[A-Za-z]:[\\/][^\s/'"<>`\\]|\/(?:home|Users)\/[^\s/'"<>]+/iu.test(text)
    || unsafeURLs(text)
    || /\bBearer\s+[A-Za-z0-9._~+/-]{20,}/i.test(text)
    || /(?:api[_-]?key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|password)["']?\s*[:=]\s*["']?(?!\$|<|example\b|placeholder\b|replace\b|process\.|null\b|false\b|true\b)[A-Za-z0-9_+./-]{12,}/i.test(text);
}

function scan(root) {
  rejectLinks(root);
  if (!lstatSync(root).isDirectory()) fail('INPUT');
  const policy = fileURLToPath(new URL('../.github/public-path-allowlist-teamai.txt', import.meta.url));
  const allowed = new Set(readFileSync(policy, 'utf8').split(/\r?\n/).map(x => x.trim()).filter(x => x && !x.startsWith('#')));
  for (const entry of allowed) {
    if (entry.includes('*') || entry.includes('\\') || entry.startsWith('/') || entry.split('/').some(x => !x || x === '.' || x === '..')) fail('INPUT');
  }
  let files = 0;
  function visit(directory) {
    for (const name of readdirSync(directory).sort()) {
      const target = path.join(directory, name);
      rejectLinks(target);
      const stat = lstatSync(target);
      const relative = path.relative(root, target).split(path.sep).join('/');
      if (/^\.env/i.test(name) || /^(?:id_rsa|id_ed25519|credentials|kubeconfig)$|\.(?:pem|p12|pfx|key)$/i.test(name)) fail('PATH');
      if (stat.isDirectory()) {
        if (![...allowed].some(item => item.startsWith(relative + '/'))) fail('PATH');
        visit(target);
        continue;
      }
      if (!stat.isFile() || !allowed.has(relative)) fail('PATH');
      if (stat.size > maximum) fail('SIZE');
      const bytes = readFileSync(target);
      if (bytes.length > maximum) fail('SIZE');
      let text;
      try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); } catch { fail('BINARY'); }
      if (/[\x00-\x08\x0b\x0c\x0e-\x1f]/.test(text)) fail('BINARY');
      if (sensitive(text)) fail('CONTENT');
      files++;
    }
  }
  visit(root);
  if (!files) fail('INPUT');
}

try {
  if (process.argv.length !== 3) fail('INPUT');
  scan(path.resolve(process.argv[2]));
  process.stdout.write('TEAMAI_PUBLIC_SCAN_OK\n');
} catch (error) {
  const code = codes.has(error.message) ? error.message : 'INPUT';
  process.stdout.write(`TEAMAI_PUBLIC_${code}\n`);
  process.exitCode = 1;
}
