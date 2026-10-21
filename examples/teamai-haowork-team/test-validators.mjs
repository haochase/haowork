import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const source = dirname(fileURLToPath(import.meta.url));
const powershell = process.env.PWSH || 'pwsh';
const powershellMissing = spawnSync(powershell, ['--version'], { timeout: 3000, stdio: 'ignore' }).error?.code === 'ENOENT';
const engines = [];
if (process.platform === 'win32' || process.env.PWSH || !powershellMissing) {
  engines.push(['powershell', powershell, ['-NoProfile', '-File'], 'validate-assets.ps1']);
} else {
  console.log('PowerShell NOT_RUN: pwsh is not installed on this Linux host');
}
const bash = process.env.BASH_EXE || (process.platform === 'win32' ? null : 'bash');
if (bash) engines.push(['bash', bash, [], 'validate-assets.sh']);
let failures = 0;
function copyAssets(from, to) {
  const metadata = join(from, '.git');
  cpSync(from, to, { recursive: true, filter: path => path !== metadata });
}
function setRepo(root, repo) {
  const path = join(root, 'teamai.yaml');
  const value = JSON.parse(readFileSync(path, 'utf8'));
  value.repo = repo;
  writeFileSync(path, JSON.stringify(value));
}
const copyTest = mkdtempSync(join(dirname(source), '.teamai-validator-test-'));
try {
  const from = join(copyTest, 'source');
  const to = join(copyTest, 'copy');
  mkdirSync(join(from, '.git'), { recursive: true });
  mkdirSync(join(from, 'docs/.git'), { recursive: true });
  writeFileSync(join(from, '.git/config'), 'source repository metadata');
  writeFileSync(join(from, 'asset.md'), 'asset content');
  copyAssets(from, to);
  const passed = !existsSync(join(to, '.git')) && existsSync(join(to, 'docs/.git')) && readFileSync(join(to, 'asset.md'), 'utf8') === 'asset content';
  console.log(`${passed ? 'PASS' : 'FAIL'} fixture-copy source-root-git-excluded`);
  if (!passed) failures++;
} finally {
  rmSync(copyTest, { recursive: true, force: true });
}
for (const [label, executable, prefix, script] of engines) {
  if (!existsSync(join(source, script))) {
    console.error(`FAIL ${label}: validator missing (negative cases cannot yet be enforced)`);
    failures++;
    continue;
  }
  const cases = [
    ['valid', () => {}, true],
    ['real-https-repo', root => setRepo(root, 'https:' + '//github.com/teamai-fixture/team-assets.git'), true],
    ['https-repo-without-suffix', root => setRepo(root, 'https:' + '//gitlab.com/teamai-fixture/team-assets'), true],
    ...[
      'http:' + '//github.com/team/repo.git',
      'file:' + '///team/repo.git',
      'https:' + '//',
      'https:' + '//github.com/',
      'https:' + '//example.invalid/',
      'https:' + '//github.com/team/repo.git?ref=main',
      'https:' + '//github.com/team/repo.git#main',
      'https:' + '//example.invalid/team/repo.git?ref=main',
      'https:' + '//user@github.com/team/repo.git',
      'https:' + '//user:pass@github.com/team/repo.git',
      'https:' + '//localhost/team/repo.git',
      'https:' + '//127.1/team/repo.git',
      'https:' + '//[::1]/team/repo.git',
      'https:' + '//git/team/repo.git',
      'https:' + '///github.com/team/repo.git',
      'https:' + '//github.com\\team/repo.git',
      'https:' + '//github.com/team/repo.git https://example.invalid/team/repo.git',
    ].map((repo, index) => [`invalid-repo-${index + 1}`, root => setRepo(root, repo), false]),
    ['missing-skill', root => rmSync(join(root, 'skills/haowork-governed-coding/SKILL.md')), false],
    ['missing-frontmatter', root => writeFileSync(join(root, 'skills/haowork-governed-coding/SKILL.md'), '# No metadata\n'), false],
    ['null-skill-name', root => {
      const path = join(root, 'skills/haowork-governed-coding/SKILL.md');
      writeFileSync(path, readFileSync(path, 'utf8').replace(/^name:.*$/m, 'name: null'));
    }, false],
    ['comment-only-description', root => {
      const path = join(root, 'skills/haowork-governed-coding/SKILL.md');
      writeFileSync(path, readFileSync(path, 'utf8').replace(/^description:.*$/m, 'description: # absent'));
    }, false],
    ['root-git-directory', root => {
      mkdirSync(join(root, '.git'));
      writeFileSync(join(root, '.git/config'), 'repository metadata only');
    }, true],
    ['root-git-file', root => writeFileSync(join(root, '.git'), 'gitdir: placeholder-metadata\n'), true],
    ['embedded-git-directory', root => mkdirSync(join(root, 'docs/.git')), false],
    ['root-git-link', (root, temp) => symlinkSync(temp, join(root, '.git'), 'junction'), false],
    ['missing-instructions', root => {
      const path = join(root, 'agents/haowork-reviewer.yaml');
      const value = JSON.parse(readFileSync(path, 'utf8'));
      delete value.instructions;
      writeFileSync(path, JSON.stringify(value));
    }, false],
    ['escaping-symlink', (root, temp) => symlinkSync(temp, join(root, 'outside'), 'junction'), false],
    ['sensitive-file', root => writeFileSync(join(root, '.env'), 'TEST_ONLY=value\n'), false],
    ['missing-team-field', root => {
      const path = join(root, 'teamai.yaml');
      const value = JSON.parse(readFileSync(path, 'utf8'));
      delete value.team;
      writeFileSync(path, JSON.stringify(value));
    }, false],
    ['credential-content', root => writeFileSync(join(root, 'docs/leak.md'), ['api', 'key'].join('_') + ' = "test-only-credential-value-12345"\n'), false],
    ['inline-json-credential', root => writeFileSync(join(root, 'docs/leak.json'), JSON.stringify({ [['api', 'key'].join('_')]: 'x' })), false],
    ['nested-json-credential', root => writeFileSync(join(root, 'docs/leak.json'), JSON.stringify({ configuration: { [['api', 'key'].join('_')]: 'test-only-credential-value-12345' } })), false],
    ['private-path', root => writeFileSync(join(root, 'docs/leak.md'), ['C:', 'Users', 'private-person', 'document.txt'].join(String.fromCharCode(92)) + '\n'), false],
  ];
  for (const [name, mutate, valid] of cases) {
    const temp = mkdtempSync(join(dirname(source), '.teamai-validator-test-'));
    try {
      const root = join(temp, 'assets');
      copyAssets(source, root);
      mutate(root, temp);
      const result = spawnSync(executable, [...prefix, join(root, script)], { encoding: 'utf8' });
      const output = `${result.stdout || ''}${result.stderr || ''}`;
      const passed = valid
        ? result.status === 0 && result.stdout.trim() === 'TEAMAI_HAOWORK_ASSETS_OK'
        : result.status !== 0 && !result.error && output.includes('TEAMAI_HAOWORK_ASSETS_INVALID') && !output.includes('test-only-credential-value-12345');
      console.log(`${passed ? 'PASS' : 'FAIL'} ${label} ${name}`);
      if (!passed) failures++;
    } finally {
      rmSync(temp, { recursive: true, force: true });
    }
  }
}
if (!bash) console.log('Bash NOT_RUN: set BASH_EXE to a working Bash interpreter');
process.exitCode = failures ? 1 : 0;
