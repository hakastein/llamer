import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { filesIn, StageStats } from '../src/agent/stream.mjs';

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
