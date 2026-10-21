import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, cpSync, readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
assert.ok(existsSync(join(root, 'scripts/validate-demo.mjs')), 'static contract validator must exist');
const temporary = mkdtempSync(join(root, '.test-'));
let count = 0;
try {
  const cases = [
    ['valid', null, true],
    ['missing platform', s => s.platforms.pop(), false],
    ['dangling task', s => s.decisions[0].tasks.push('missing'), false],
    ['dangling decision', s => s.tasks[0].decisions.push('missing'), false],
    ['missing root cause', s => delete s.requirement.root_cause, false],
    ['empty tasks', s => { s.tasks = []; }, false],
    ['empty decisions', s => { s.decisions = []; }, false],
    ['empty plan', s => { s.tasks = []; s.decisions = []; }, false],
    ['missing scope', s => delete s.scope, false],
    ['missing owners', s => delete s.owners, false],
    ['missing checks', s => delete s.checks, false],
    ['empty scope', s => { s.scope = []; }, false],
    ['empty owners', s => { s.owners = []; }, false],
    ['empty checks', s => { s.checks = []; }, false],
    ['wrong upgrade', s => s.gates.report_can_complete = true, false],
    ['sensitive file', null, false],
    ['sensitive content', null, false],
  ];
  for (const [name, mutate, expected] of cases) {
    const target = join(temporary, String(count++));
    mkdirSync(target);
    for (const part of ['project', 'teamai', 'evidence', 'README.md']) cpSync(join(root, part), join(target, part), { recursive: true });
    const path = join(target, 'project/scenario.json');
    const scenario = JSON.parse(readFileSync(path, 'utf8'));
    if (mutate) mutate(scenario);
    writeFileSync(path, JSON.stringify(scenario));
    if (name === 'sensitive file') writeFileSync(join(target, '.env'), 'EMPTY');
    if (name === 'sensitive content') writeFileSync(join(target, 'project/leak.md'), ['api', 'key'].join('_') + '=sk-' + 'x'.repeat(30));
    const result = spawnSync(process.execPath, [join(root, 'scripts/validate-demo.mjs'), target], { encoding: 'utf8' });
    assert.equal(result.status === 0, expected, `${name}: ${result.stdout}${result.stderr}`);
    console.log(`PASS ${name}`);
  }
} finally {
  rmSync(temporary, { recursive: true, force: true });
}
console.log(`PASS ${count} static contract cases`);
