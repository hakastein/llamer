import { test } from 'node:test';
import assert from 'node:assert/strict';
import { matchesAnyMask } from '../src/materials/pathMask.mjs';

test('маска пути покрывает файлы изменений', () => {
  assert.equal(matchesAnyMask(['src/**'], ['src/Finances/Ledger.php']), true);
  assert.equal(matchesAnyMask(['src/**'], ['public/index.php']), false);
  assert.equal(matchesAnyMask(['**/*.php'], ['src/A.php']), true);
  assert.equal(matchesAnyMask(['**/*.php'], ['src/A.vue']), false);
  assert.equal(matchesAnyMask(['**/*Repository.php'], ['src/X/FooRepository.php']), true);
  assert.equal(matchesAnyMask(['**/*Repository.php'], ['src/X/Foo.php']), false);
  assert.equal(matchesAnyMask(['tests/e2e/**'], ['tests/e2e/specs/a.spec.ts']), true);
  assert.equal(matchesAnyMask(['**'], ['anything/at/all.md']), true);
});

test('пустой список масок покрывает всё, пустой список файлов — ничего', () => {
  assert.equal(matchesAnyMask([], ['src/A.php']), true);
  assert.equal(matchesAnyMask(['src/**'], []), false);
});

test('маска с ! исключает файлы из подошедших, как в .gitignore', () => {
  const masks = ['tests/**', '!tests/e2e/**'];

  assert.equal(matchesAnyMask(masks, ['tests/Unit/OrderTest.php']), true);
  assert.equal(matchesAnyMask(masks, ['tests/e2e/specs/a.spec.ts']), false);
  assert.equal(matchesAnyMask(masks, ['src/A.php']), false);
  assert.equal(matchesAnyMask(['!docs/**'], ['src/A.php']), true);
});

test('звёздочка не перескакивает через разделитель каталогов', () => {
  assert.equal(matchesAnyMask(['src/*.php'], ['src/deep/A.php']), false);
  assert.equal(matchesAnyMask(['src/*.php'], ['src/A.php']), true);
});
