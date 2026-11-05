import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { filesIn, StageStats } from '../src/agent/stream.mjs';
import { codexNormalizer } from '../src/drivers/codex.mjs';

function workspace(t) {
  const cwd = mkdtempSync(join(tmpdir(), 'llamer-stream-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  return cwd;
}

test('filesIn пропускает кандидатов в пути, у которых родитель — обычный файл', (t) => {
  const cwd = workspace(t);
  mkdirSync(join(cwd, 'build'));
  writeFileSync(join(cwd, 'build/i18n-chunks.js'), '');
  writeFileSync(join(cwd, 'build/i18n-routing.js'), '');

  for (const path of [
    'build/i18n-chunks.js/i18n-routing.js',
    join(cwd, 'build/i18n-chunks.js/i18n-routing.js'),
  ]) {
    const command = `rg '${path}' build missing.js build/i18n-chunks.js '${cwd}/build/i18n-routing.js'`;
    assert.deepEqual(filesIn(command, cwd), ['build/i18n-chunks.js', 'build/i18n-routing.js']);
  }
});

test('StageStats дочитывает поток, когда в аргументах Bash путь сквозь обычный файл', (t) => {
  const cwd = workspace(t);
  mkdirSync(join(cwd, 'build'));
  writeFileSync(join(cwd, 'build/i18n-chunks.js'), '');

  const stats = StageStats.from([{
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        name: 'Bash',
        input: { command: 'rg "build/i18n-chunks.js/i18n-routing.js" build/i18n-chunks.js' },
      }],
    },
  }], { cwd });

  assert.deepEqual([...stats.files], ['build/i18n-chunks.js']);
  assert.equal(stats.calls.get('Bash'), 1);
  assert.ok(stats.lines().includes('прочитано файлов: 1'));
});

test('filesIn не глотает ошибки файловой системы, кроме несуществующих кандидатов', (t) => {
  const cwd = workspace(t);
  symlinkSync('loop', join(cwd, 'loop'));

  assert.throws(() => filesIn('cat loop', cwd), { code: 'ELOOP' });
});

const call = (id, command) => ({ type: 'assistant', message: { content: [{ type: 'tool_use', id, name: 'Bash', input: { command } }] } });
const answer = (id, content) => ({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: id, is_error: true, content }] } });

test('StageStats считает вызовы харнесса, не дошедшие до журнала, — у claude и у codex', () => {
  const claude = StageStats.from([
    call('t1', "review batch --json '[\n{}\n]'"),
    answer('t1', 'Permission to use Bash has been denied because Claude Code is running in don\'t ask mode.'),
    // Отказ харнесса агент чинит повтором — это не потеря.
    call('t2', "review finding --file a --line 1 --text 'x'"),
    answer('t2', 'Exit code 1\nотказ: строка 1 в a не изменена'),
    call('t3', 'rg missing'),
    answer('t3', 'Exit code 1'),
  ]);
  assert.equal(claude.lostWrites, 1);
  assert.equal(claude.errors, 3);

  const normalize = codexNormalizer('m');
  const codex = StageStats.from([
    { type: 'item.started', item: { id: 'c1', type: 'command_execution', command: "review batch --json '[]'" } },
    {
      type: 'item.completed',
      item: {
        id: 'c1', type: 'command_execution', exit_code: 2, aggregated_output: 'сбой харнесса: EROFS: read-only file system',
      },
    },
  ].flatMap(normalize));
  assert.equal(codex.lostWrites, 1);
});
