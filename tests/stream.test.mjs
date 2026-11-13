import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { filesIn, StageStats } from '../src/agent/stream.mjs';
import { codexNormalizer, requestCost, sessionTotals } from '../src/drivers/codex.mjs';
import { WRITE_COMMANDS } from '../src/harness/createCommands.mjs';

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
    // Справка и чтение харнесса записи не несут: упала соседняя команда, а не запись.
    call('t4', 'review --help; lsd def X'),
    answer('t4', 'Exit code 5'),
    call('t5', 'review diff a.php'),
    answer('t5', 'Exit code 1'),
    // Запись после чтения в той же команде — запись.
    call('t6', "review list && review finding --file a --line 1 --text 'x'"),
    answer('t6', 'Permission to use Bash has been denied'),
  ], { writes: WRITE_COMMANDS });
  assert.equal(claude.lostWrites, 2);
  assert.equal(claude.errors, 6);

  const normalize = codexNormalizer('m');
  const codex = StageStats.from([
    { type: 'item.started', item: { id: 'c1', type: 'command_execution', command: "/bin/bash -lc 'review batch --json []'" } },
    {
      type: 'item.completed',
      item: {
        id: 'c1', type: 'command_execution', exit_code: 2, aggregated_output: 'сбой харнесса: EROFS: read-only file system',
      },
    },
  ].flatMap(normalize), { writes: WRITE_COMMANDS });
  assert.equal(codex.lostWrites, 1);
});

test('цена запроса codex: Standard по прейскуранту, промпт длиннее 272K — вдвое за вход и кэш и в полтора раза за выход', () => {
  const near = (actual, expected) => assert.ok(Math.abs(actual - expected) < 1e-9, `${actual} ≠ ${expected}`);
  near(requestCost('gpt-6.1-terra', { input_tokens: 100_000, cached_input_tokens: 0, output_tokens: 100_000 }), 0.6);
  near(requestCost('gpt-6.1-sol', { input_tokens: 272_000, cached_input_tokens: 72_000, output_tokens: 1000 }), 0.4172);
  // 200K × $2 + 100K × $0.10 = $0.41 вдвое, 1K × $10 в полтора раза
  near(requestCost('gpt-6.1-sol', { input_tokens: 300_000, cached_input_tokens: 100_000, output_tokens: 1000 }), 0.835);
  assert.equal(requestCost('gpt-неизвестная', { input_tokens: 1 }), null);

  const requests = [{ input_tokens: 9000, output_tokens: 10 }, { input_tokens: 12_000, output_tokens: 10 }];
  assert.equal(sessionTotals('gpt-6.1-terra', requests).contextTokens, 12_000, 'пик контекста — самый длинный запрос');
  near(sessionTotals('gpt-6.1-terra', requests).cost, 0.0211);
  assert.equal(sessionTotals('gpt-неизвестная', requests).cost, null, 'цены нет — $?, а не $0');
  assert.deepEqual(sessionTotals('gpt-6.1-terra', []), { contextTokens: 0, cost: null }, 'журнала нет — цена неизвестна');
});
