import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fileURLToPath(new URL('./check-teamai-public.mjs', import.meta.url));
const root = path.resolve(path.dirname(script), '..');
const temporary = path.join(root, '.cache');
mkdirSync(temporary, { recursive: true });

test('public promotion includes shared integration points and independent public CI', () => {
  const policy = readFileSync(path.join(root, '.github/public-path-allowlist-teamai.txt'), 'utf8').split(/\r?\n/);
  for (const file of ['internal/model/model.go', 'internal/model/reducer.go', 'internal/cli/project_commands.go', '.github/workflows/teamai-public-contract.yml']) {
    assert.ok(policy.includes(file), 'required public integration file missing');
  }
  assert.ok(!policy.includes('.github/workflows/teamai-contract.yml'));
  const workflow = JSON.parse(readFileSync(path.join(root, '.github/workflows/teamai-public-contract.yml'), 'utf8'));
  assert.deepEqual(workflow.permissions, { contents: 'read' });
  assert.deepEqual(workflow.jobs.contract.strategy.matrix.os, ['windows-latest', 'ubuntu-latest']);
  const commands = workflow.jobs.contract.steps.filter(step => step.run).map(step => step.run);
  assert.ok(commands.includes("go test ./internal/teamai ./internal/model ./internal/app ./internal/cli -run 'TestPublicTeamAI|TestTeamAI' -count=1"));
  assert.ok(commands.includes('node --test scripts/check-teamai-public.test.mjs'));
  assert.ok(!commands.some(command => /p0-05|preflight|cluster-test|internal\/e2e|executionreceipt/.test(command)));
});

function candidate(run) {
  const dir = mkdtempSync(path.join(temporary, 'public-test-'));
  try { run(dir); } finally { rmSync(dir, { recursive: true, force: true }); }
}

function check(dir, expected, executable = process.execPath, args = [script]) {
  const result = spawnSync(executable, [...args, dir], { encoding: 'utf8' });
  assert.equal(result.status, expected === 'TEAMAI_PUBLIC_SCAN_OK' ? 0 : 1);
  assert.equal(result.stdout.trim(), expected);
  assert.equal(result.stderr, '');
}

const cases = [
  ['outside allowlist', 'private.txt', 'private', 'TEAMAI_PUBLIC_PATH'],
  ['personal path', 'README.md', ['E:', 'haochase', 'private'].join('/'), 'TEAMAI_PUBLIC_CONTENT'],
  ['token', 'README.md', 'ghp_' + 'a'.repeat(36), 'TEAMAI_PUBLIC_CONTENT'],
  ['environment', '.env.local', 'secret', 'TEAMAI_PUBLIC_PATH'],
  ['internal URL', 'README.md', 'https://' + ['10', '2', '3', '4'].join('.') + '/api', 'TEAMAI_PUBLIC_CONTENT'],
  ['private key', 'README.md', '-----BEGIN ' + 'PRIVATE KEY-----', 'TEAMAI_PUBLIC_CONTENT'],
  ['encrypted private key', 'README.md', '-----BEGIN ' + 'ENCRYPTED PRIVATE KEY-----', 'TEAMAI_PUBLIC_CONTENT'],
  ['Slack token', 'README.md', 'xoxb-' + '123456789012-123456789012-' + 'a'.repeat(24), 'TEAMAI_PUBLIC_CONTENT'],
  ['Unicode drive path', 'README.md', ['E:', '私人', '账户', 'config'].join('\\'), 'TEAMAI_PUBLIC_CONTENT'],
  ['mapped private IPv6', 'README.md', 'http://' + '[::ffff:' + ['192', '168', '1', '2'].join('.') + ']/api', 'TEAMAI_PUBLIC_CONTENT'],
  ['IPv4 shorthand', 'README.md', 'http://' + '127.1/api', 'TEAMAI_PUBLIC_CONTENT'],
  ['integer IP URL', 'README.md', 'http://' + '2130706433/api', 'TEAMAI_PUBLIC_CONTENT'],
  ['real config', 'README.md', 'api_key: ' + 'live-secret-value', 'TEAMAI_PUBLIC_CONTENT'],
  ['generic token assignment', 'README.md', 'token = ' + 'live-secret-value', 'TEAMAI_PUBLIC_CONTENT'],
  ['bearer credential', 'README.md', 'Bearer ' + 'a'.repeat(40), 'TEAMAI_PUBLIC_CONTENT'],
  ['other personal drive path', 'README.md', ['D:', 'projects', 'private-user', 'notes'].join('/'), 'TEAMAI_PUBLIC_CONTENT'],
  ['service endpoint', 'README.md', 'base_url: https://' + 'model.company.example/v1', 'TEAMAI_PUBLIC_CONTENT'],
  ['oversized', 'README.md', 'x'.repeat(1024 * 1024 + 1), 'TEAMAI_PUBLIC_SIZE'],
  ['binary', 'README.md', Buffer.from([65, 0, 66]), 'TEAMAI_PUBLIC_BINARY'],
];
for (const [name, file, content, expected] of cases) {
  test(name, () => candidate(dir => {
    writeFileSync(path.join(dir, file), content);
    check(dir, expected);
  }));
}
test('valid explanatory text and public showcase', () => candidate(dir => {
  writeFileSync(path.join(dir, 'README.md'), 'API Key is a term. https://example.invalid/team https://haowork.112318.xyz/');
  check(dir, 'TEAMAI_PUBLIC_SCAN_OK');
}));
test('quoted placeholder endpoint without trailing slash is allowed', () => candidate(dir => {
  writeFileSync(path.join(dir, 'README.md'), 'endpoint: "https://example.invalid"');
  check(dir, 'TEAMAI_PUBLIC_SCAN_OK');
}));
test('empty candidate is not a successful publication check', () => candidate(dir => {
  check(dir, 'TEAMAI_PUBLIC_INPUT');
}));
test('reject directory symlink or Windows junction without leaking target', () => candidate(dir => {
  const target = path.join(dir, 'target');
  mkdirSync(target);
  symlinkSync(target, path.join(dir, 'docs'), process.platform === 'win32' ? 'junction' : 'dir');
  check(dir, 'TEAMAI_PUBLIC_LINK');
}));
test('reject linked candidate root', () => candidate(dir => {
  const target = path.join(dir, 'target');
  mkdirSync(target);
  const link = path.join(dir, 'link');
  symlinkSync(target, link, process.platform === 'win32' ? 'junction' : 'dir');
  check(link, 'TEAMAI_PUBLIC_LINK');
}));
test('reject ancestor junction', () => candidate(dir => {
  const target = path.join(dir, 'target');
  mkdirSync(path.join(target, 'child'), { recursive: true });
  const link = path.join(dir, 'link');
  symlinkSync(target, link, process.platform === 'win32' ? 'junction' : 'dir');
  check(path.join(link, 'child'), 'TEAMAI_PUBLIC_LINK');
}));
const wrappers = [
  ['PowerShell', process.env.PWSH || 'pwsh', ['-NoProfile', '-File', script.replace(/\.mjs$/, '.ps1'), '-CandidateDirectory']],
  ['Bash', process.env.BASH_EXE || (process.platform === 'win32' ? null : 'bash'), [script.replace(/\.mjs$/, '.sh')]],
];
for (const [name, executable, args] of wrappers) {
  const optionalPowerShell = name === 'PowerShell' && process.platform !== 'win32' && !process.env.PWSH;
  const unavailable = executable && spawnSync(executable, ['--version'], { timeout: 3000, stdio: 'ignore' }).error?.code === 'ENOENT';
  const skip = !executable || (optionalPowerShell && unavailable);
  test(`${name} wrapper accepts candidate and rejects secret without disclosure`, { skip }, () => candidate(dir => {
    writeFileSync(path.join(dir, 'README.md'), 'API Key terminology only');
    check(dir, 'TEAMAI_PUBLIC_SCAN_OK', executable, args);
    writeFileSync(path.join(dir, 'README.md'), 'ghp_' + 'b'.repeat(36));
    check(dir, 'TEAMAI_PUBLIC_CONTENT', executable, args);
  }));
  test(`${name} wrapper missing argument has a stable diagnostic`, { skip }, () => {
    const invocation = name === 'PowerShell' ? args.slice(0, -1) : args;
    const result = spawnSync(executable, invocation, { encoding: 'utf8', timeout: 10000 });
    assert.equal(result.status, 1);
    assert.equal(result.stdout.trim(), 'TEAMAI_PUBLIC_INPUT');
    assert.equal(result.stderr, '');
  });
}
