import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  existsSync, readdirSync, readFileSync, utimesSync,
} from 'node:fs';
import { join } from 'node:path';
import {
  CONFIG, SECRETS, git, levels, makeRepo, run, write,
} from './fixtures/repo.mjs';

const stagesOf = (launches) => launches.map((launch) => launch.stage).sort();

test('--local: конвейер на заглушке claude доходит до report.md и JSON находок в stdout', async () => {
  const root = makeRepo();
  const statusBefore = git(root, 'status', '--porcelain');

  const result = await run(root, ['--local']);

  assert.equal(result.status, 0, result.stderr);
  const answer = JSON.parse(result.stdout);
  assert.equal(answer.version, 2);
  assert.equal(answer.ok, true);
  assert.equal(answer.input, 'local');
  assert.equal(answer.assessment.level, 2);
  // Два разбирающих этапа отметили одно место — дедупликация оставила одну находку.
  assert.equal(answer.findings.length, 1);
  assert.deepEqual(answer.summaries.map((summary) => summary.stage).sort(), ['logic']);
  assert.match(readFileSync(answer.report, 'utf-8'), /## Находки: 1/);

  // Токен, напечатанный заглушкой в находке, не доходит ни до отчёта, ни до журналов.
  const journals = join(answer.artifacts, 'journal');
  const written = [result.stdout, readFileSync(answer.report, 'utf-8'),
    ...readdirSync(journals).map((name) => readFileSync(join(journals, name), 'utf-8'))];
  assert.match(answer.findings[0].text, /токен \*\*\*REDACTED\*\*\*/);
  assert.ok(written.every((text) => !text.includes(SECRETS.GITLAB_TOKEN)), 'токен в отчёте или журнале');

  // Этапы docs и tests не поднялись: файлов под их маски в изменениях нет. Тредов локально нет.
  assert.deepEqual(stagesOf(result.launches), ['assess', 'comments', 'dedupe', 'logic', 'quality', 'skeptic']);
  assert.equal(git(root, 'status', '--porcelain'), statusBefore, 'индекс и рабочее дерево разработчика не тронуты');
});

test('--local разбирает от точки расхождения до рабочего дерева, без чужих правок целевой ветки', async () => {
  const root = makeRepo();

  const { stdout } = await run(root, ['--local']);

  const diff = readFileSync(join(JSON.parse(stdout).artifacts, 'task.diff'), 'utf-8');
  assert.match(diff, /\+\s+return 3;/, 'незакоммиченная правка в разборе');
  assert.match(diff, /b\/src\/Fresh\.php/, 'неотслеживаемый файл в разборе');
  assert.doesNotMatch(diff, /Foreign\.php/, 'правка целевой ветки после ответвления не в разборе');
});

test('--local: опровергнутая скептиком находка с токеном пишется в лог вымаранной', async () => {
  const root = makeRepo({ config: { ...CONFIG, drivers: { claude: { env: { STUB_SKEPTIC_REFUTES: '1' } } } } });

  const { stdout, status, stderr } = await run(root, ['--local']);

  assert.equal(status, 0, stderr);
  assert.equal(JSON.parse(stdout).findings.length, 0);
  assert.match(stderr, /скептик опроверг находок: 1/);
  assert.match(stderr, /токен \*\*\*REDACTED\*\*\*/);
  assert.ok(!stderr.includes(SECRETS.GITLAB_TOKEN), 'токен в stderr');
});

test('--local: правка той же длины в секунду записи индекса попадает в снимок (racy git)', async () => {
  const root = makeRepo();
  // У файла и индекса одно время: по времени и размеру файл неотличим от
  // записанного в индекс, и правку видит только перечитывание «гоночных» записей.
  // ctime не выставить — отключаем его сверку через core.trustctime.
  const file = join(root, 'src/Order.php');
  const past = new Date('2020-01-01T00:00:00Z');
  git(root, 'config', 'core.trustctime', 'false');
  utimesSync(file, past, past);
  git(root, 'add', 'src/Order.php');
  write(root, 'src/Order.php', '<?php\n\nfunction total() {\n    return 5;\n}\n');
  utimesSync(file, past, past);
  utimesSync(join(root, '.git/index'), past, past);

  const { stdout, status, stderr } = await run(root, ['--local']);

  assert.equal(status, 0, stderr);
  assert.match(readFileSync(join(JSON.parse(stdout).artifacts, 'task.diff'), 'utf-8'), /\+\s+return 5;/);
});

test('этап claude изолирован: свои разрешения, окружение — белый список, общий пакет один на все этапы', async () => {
  const root = makeRepo({ config: { ...CONFIG, levels: levels() } });

  const { launches, status, stderr } = await run(root, ['--local'], {
    CI_REGISTRY_PASSWORD: 'registry-password-value', HTTPS_PROXY: 'http://proxy.example.com:3128',
  });

  assert.equal(status, 0, stderr);
  const shared = new Set();
  for (const { stage, argv, env, configDir, shared: pack } of launches) {
    assert.equal(argv[argv.indexOf('--setting-sources') + 1], 'project', stage);
    assert.equal(argv[argv.indexOf('--permission-mode') + 1], 'dontAsk', stage);
    assert.ok(argv.includes('--strict-mcp-config'), stage);
    assert.ok(argv.includes('--plugin-dir'), stage);
    for (const name of [...Object.keys(SECRETS), 'CI_REGISTRY_PASSWORD']) {
      assert.ok(!env.includes(name), `${stage}: ${name} в окружении этапа`);
    }
    assert.ok(env.includes('CLAUDE_CODE_OAUTH_TOKEN'), `${stage}: вход драйвера в окружении этапа`);
    assert.ok(env.includes('HTTPS_PROXY'), `${stage}: прокси в окружении этапа`);
    assert.match(configDir, /llamer-run-/, `${stage}: личный конфиг claude`);
    const allowed = argv[argv.indexOf('--allowedTools') + 1];
    assert.ok(allowed.includes('Bash(rg *)'), `${stage}: общий allow`);
    assert.equal(allowed.includes('Bash(git log *)'), stage === 'logic', `${stage}: allow этапа только у него`);
    if (pack !== null) shared.add(pack);
  }
  assert.equal(shared.size, 1, 'общая часть пакета разошлась между этапами — кэш не поделится');
  assert.match([...shared][0], /<request_materials>/);

  const byStage = Object.fromEntries(launches.map((launch) => [launch.stage, launch.argv]));
  const modelOf = (argv) => `${argv[argv.indexOf('--model') + 1]}/${argv[argv.indexOf('--effort') + 1]}`;
  assert.equal(modelOf(byStage.assess), 'model-1/effort-1', 'оценщик — уровень 1');
  assert.equal(modelOf(byStage.logic), 'model-2/effort-2', 'logic идёт на уровне оценщика (2)');
  assert.ok(byStage.logic.includes('--append-system-prompt-file'));
  assert.ok(!byStage.dedupe.includes('--append-system-prompt-file'), 'дедупликации пакет не нужен');

  const logic = launches.find((launch) => launch.stage === 'logic').prompt;
  assert.match(logic, /- stage:reviewing-business-logic\n- stage:reviewing\n/, 'always: встроенный и добавленный конфигом');
  assert.doesNotMatch(logic, /backend/, 'onDemand в задании не называется: claude видит скиллы плагина сам');
});

test('--local на драйвере codex: read-only, временные HOME и CODEX_HOME, журнал пишет правилом', async () => {
  const root = makeRepo({
    config: {
      ...CONFIG,
      levels: levels({
        1: 'codex', 2: 'codex', 3: 'codex', 4: 'codex', 5: 'codex',
      }).map((level) => ({ ...level, model: 'gpt-6.1-terra' })),
    },
  });

  const result = await run(root, ['--local']);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).findings.length, 1);
  assert.deepEqual(stagesOf(result.launches), ['assess', 'comments', 'dedupe', 'logic', 'quality', 'skeptic']);
  for (const launch of result.launches) {
    assert.equal(launch.driver, 'codex');
    assert.deepEqual(launch.argv.slice(0, 4), ['exec', '--json', '--sandbox', 'read-only'], launch.stage);
    assert.ok(!launch.argv.includes('--ignore-user-config'), `${launch.stage}: config.toml этапа — свой, его надо читать`);
    assert.ok(launch.argv.includes('skills.bundled.enabled=false'), `${launch.stage}: встроенные скиллы codex отключены`);
    assert.notEqual(launch.home, result.env.HOME, `${launch.stage}: личный HOME`);
    assert.notEqual(launch.codexHome, result.env.CODEX_HOME, `${launch.stage}: личный CODEX_HOME`);
    assert.ok(launch.auth, `${launch.stage}: вход codex не скопирован`);
    assert.match(launch.rules, /prefix_rule\(pattern=\["review"\], decision="allow"\)/);
    assert.match(launch.rules, /pattern=\["rg"\]/);
    assert.equal(/pattern=\["git","log"\]/.test(launch.rules), launch.stage === 'logic', launch.stage);
    for (const name of Object.keys(SECRETS)) assert.ok(!launch.env.includes(name), `${launch.stage}: ${name}`);
  }
  const logic = result.launches.find((launch) => launch.stage === 'logic');
  assert.match(logic.config, /^developer_instructions = "What follows is the material of a merge request/, 'общий пакет — developer_instructions');
  assert.match(logic.prompt, /^You are a Senior Software Engineer/, 'в задании пакета нет');
  assert.equal(result.launches.find((launch) => launch.stage === 'dedupe').config, '', 'дедупликации пакет не нужен');
  assert.match(logic.prompt, /closed as `values\['"'"'severity'"'"'\]`/, 'кавычка — в форме, которую признают правила codex');
  assert.match(logic.prompt, /- \$reviewing-business-logic\n- \$reviewing\n/, 'always — упоминанием $имя, оно подключает скилл');
  assert.doesNotMatch(logic.prompt, /backend/, 'onDemand в задании не называется: упоминание подключило бы его без выбора');
  for (const name of ['reviewing', 'backend']) {
    assert.ok(logic.skills?.includes(name), `${name}: codex не найдёт скилл в $HOME/.agents/skills`);
  }

  // Цена и пик контекста — по запросам из журнала сессии заглушки: 2000 и 3000 токенов входа.
  const cost = JSON.parse(readFileSync(join(JSON.parse(result.stdout).artifacts, 'cost.json'), 'utf-8'));
  for (const stage of cost.stages) {
    assert.equal(stage.contextTokens, 3000, stage.name);
    assert.ok(Math.abs(stage.cost - 0.0027) < 1e-9, `${stage.name}: ${stage.cost}`);
  }
});


test('этап вышел без сводки или с отклонённым вызовом харнесса — сорван: код 1, причина в отчёте', async () => {
  for (const [knob, stage, reason] of [
    ['STUB_NO_SUMMARY', 'logic', 'сводка не записана'],
    ['STUB_DENY_STAGE', 'quality', 'вызовов харнесса не дошло до журнала: 1'],
  ]) {
    const root = makeRepo({ config: { ...CONFIG, drivers: { claude: { env: { [knob]: stage } } } } });

    const result = await run(root, ['--local']);

    assert.equal(result.status, 1, knob);
    const answer = JSON.parse(result.stdout);
    assert.ok(answer.gaps.includes(`этап «${stage}» сорвался: ${reason}`), `${knob}: ${answer.gaps}`);
    const report = readFileSync(answer.report, 'utf-8');
    assert.match(report, new RegExp(`## Недостача\\n\\n- этап «${stage}» сорвался: ${reason}`), knob);
    assert.match(report, new RegExp(`${stage} \\(${reason}\\)`), `${knob}: этап помечен в таблице цены`);
  }
});

test('--base/--head: голова выкладывается отдельным деревом и убирается после прогона', async () => {
  const root = makeRepo();
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'finish');
  const head = git(root, 'rev-parse', 'HEAD');
  const base = git(root, 'merge-base', 'main', 'HEAD');
  git(root, 'checkout', '-q', 'main');

  const result = await run(root, ['--base', base, '--head', head]);

  assert.equal(result.status, 0, result.stderr);
  const answer = JSON.parse(result.stdout);
  assert.equal(answer.input, 'range');
  assert.equal(answer.base, base);
  assert.deepEqual(answer.findings.map((finding) => finding.file), ['src/Fresh.php']);
  assert.match(result.stderr, /выложена в/);
  assert.equal(git(root, 'worktree', 'list').split('\n').length, 1, 'временное дерево убрано');
  assert.equal(git(root, 'rev-parse', '--abbrev-ref', 'HEAD'), 'main');
});

test('нет токена трекера — preflight сообщает пробел, а прогон идёт без задачи со строкой в отчёте', async () => {
  // Номер задачи в ветке есть (feature/dev-7-orders), текста задачи нет.
  const root = makeRepo({ config: { ...CONFIG, tracker: { type: 'youtrack', issuePattern: 'DEV-\\d+' } } });
  const env = { YOUTRACK_URL: 'http://127.0.0.1:9', YOUTRACK_TOKEN: '' };

  const preflight = JSON.parse((await run(root, ['--local', '--preflight'], env)).stdout);
  assert.deepEqual(preflight.gaps.map((gap) => [gap.id, gap.blocking]), [['tracker', false]]);
  assert.equal(preflight.ok, true);

  const result = await run(root, ['--local'], env);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /предупреждение: трекер youtrack: не заданы YOUTRACK_TOKEN/);
  const answer = JSON.parse(result.stdout);
  assert.match(answer.gaps[0], /YOUTRACK_TOKEN — разбор идёт без текста задачи/);
  assert.match(readFileSync(answer.report, 'utf-8'), /## Недостача\n\n- трекер youtrack/);
  assert.match(result.stderr, /задача: DEV-7/);
  const logic = result.launches.find((launch) => launch.stage === 'logic');
  assert.doesNotMatch(logic.prompt, /DEV-7|в пакете ниже/, 'этапу обещана задача, которой в пакете нет');
});

test('--preflight: пробелы окружения JSON-ом, без флага прогон не стартует', async () => {
  const config = { ...CONFIG, tracker: { type: 'youtrack', issuePattern: 'PROJ-\\d+' } };
  config.stages = { ...CONFIG.stages, extra: { skills: { always: ['missing-skill'] }, task: 'Что-то ещё.' } };
  const root = makeRepo({ config });

  const preflight = await run(root, ['--local', '--preflight'], { YOUTRACK_URL: '' });
  assert.equal(preflight.status, 1);
  const answer = JSON.parse(preflight.stdout);
  assert.equal(answer.ok, false);
  assert.deepEqual(answer.gaps.map((gap) => gap.id).sort(), ['skills', 'tracker']);

  const refused = await run(root, ['--local'], { YOUTRACK_URL: '' });
  assert.equal(refused.status, 1);
  assert.match(refused.stderr, /окружение не готово/);
  assert.equal(refused.launches.length, 0);
  assert.equal(existsSync(join(root, '.llamer')), false, 'до проверки окружения артефактов нет');
});

test('preflight проверяет драйверы из levels: неработающий бинарь и отсутствие входа', async () => {
  const drivers = { codex: { bin: 'codex-нет-такого' } };
  const gapsOf = async (config) => JSON.parse((await run(makeRepo({ config }), ['--local', '--preflight'], { CLAUDE_CODE_OAUTH_TOKEN: '' })).stdout)
    .gaps.map((gap) => gap.id).sort();

  assert.deepEqual(await gapsOf({ ...CONFIG, drivers }), ['auth-claude'], 'codex нет в levels — не проверяется');
  assert.deepEqual(await gapsOf({ ...CONFIG, drivers, levels: levels({ 5: 'codex' }) }), ['auth-claude', 'driver-codex']);
});

test('конфиг из одной версии: встроенные этапы идут со встроенными скиллами', async () => {
  const root = makeRepo({ config: { version: 1 } });

  const preflight = await run(root, ['--local', '--preflight']);
  assert.equal(preflight.status, 0, preflight.stderr);
  assert.deepEqual(JSON.parse(preflight.stdout).gaps, []);

  const result = await run(root, ['--local']);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(stagesOf(result.launches), ['assess', 'comments', 'dedupe', 'logic', 'quality', 'skeptic']);
  const prompts = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.prompt]));
  assert.match(prompts.assess, /- stage:assessing-review-scope/);
  assert.match(prompts.logic, /- stage:reviewing-business-logic/);
  assert.match(prompts.quality, /- stage:reviewing-code-quality/);
  assert.match(prompts.skeptic, /- stage:refuting-review-findings/);
  assert.match(prompts.skeptic, /written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer and the test reviewer\./);
  const plugin = join(JSON.parse(result.stdout).artifacts, 'stage-plugin', 'skills');
  assert.deepEqual(readdirSync(plugin).sort(), ['assessing-review-scope', 'refuting-review-findings', 'reviewing-business-logic', 'reviewing-code-quality']);
});

test('встроенный разбирающий этап идёт своим шаблоном старого движка, свой этап — общим с его task', async () => {
  const root = makeRepo();
  write(root, 'docs/guide.md', 'Руководство.\n');
  write(root, 'tests/OrderTest.php', '<?php\n\n// Проверяет сумму заказа.\nfunction testTotal() {}\n');

  const result = await run(root, ['--local']);

  assert.equal(result.status, 0, result.stderr);
  const prompts = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.prompt]));
  assert.match(prompts.logic, /^You are a Senior Software Engineer with expertise in domain logic/);
  assert.match(prompts.quality, /^You are a Senior Software Engineer with expertise in readability/);
  assert.match(prompts.comments, /^You are a Senior Software Engineer with expertise in code documentation/);
  assert.match(prompts.comments, /<added_comments>\ntests\/OrderTest\.php\nL3\+\t\/\/ Проверяет сумму заказа\.\nL4\tfunction testTotal\(\) \{\}/);
  assert.match(prompts.tests, /^You are a Senior Test Engineer/);
  assert.match(prompts.tests, /Files:\n\n- tests\/OrderTest\.php\n\n/, 'тестам — только файлы под их маски');
  assert.match(prompts.docs, /^Документация\.\n\nFindings outside this task are not yours/);
  // Сводку пишут бизнес-логика и свой этап, как reviewer старого движка; остальные разбирающие — без неё.
  const summary = /\$ review summary --help/;
  for (const stage of ['logic', 'docs']) assert.match(prompts[stage], summary, stage);
  for (const stage of ['quality', 'comments', 'tests']) assert.doesNotMatch(prompts[stage], summary, stage);
  assert.match(prompts.skeptic, /written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer, the test reviewer and the docs stage\./);
});

test('prompt конфига: строка протокола — в «How This Run Works», план заменяет поиск по тексту; без него — поиск', async () => {
  const config = {
    ...CONFIG,
    prompt: { protocol: '`nav` is up.', plan: 'Plan with `nav def`.' },
    stages: { ...CONFIG.stages, quality: { prompt: { plan: 'Quality plan.' } } },
  };
  const prompts = Object.fromEntries((await run(makeRepo({ config }), ['--local'])).launches
    .map((launch) => [launch.stage, launch.prompt]));
  const plain = Object.fromEntries((await run(makeRepo(), ['--local'])).launches.map((launch) => [launch.stage, launch.prompt]));

  for (const stage of ['assess', 'logic', 'skeptic']) {
    assert.match(prompts[stage], /such files open by direct path\.\n`nav` is up\.\nLinters, tests/, stage);
  }
  assert.match(prompts.logic, /## Your Plan\n\nPlan with `nav def`\.\n\nThen work through the plan\./);
  assert.match(prompts.quality, /## Your Plan\n\nQuality plan\.\n\nThen work through the plan\./);
  assert.doesNotMatch(prompts.logic, /or a text search/);
  assert.match(plain.logic, /direct path\.\nLinters, tests/);
  assert.match(plain.logic, /## Your Plan\n\nPresent a plan before your first search: [^\n]*, or a text search\./);
});

test('этап идёт драйвером своего уровня', async () => {
  // Уровень 1 — codex: оценщик, комментарии и дедупликация на нём; logic и скептик — на уровне оценщика (2), quality — на 2.
  const root = makeRepo({ config: { ...CONFIG, levels: levels({ 1: 'codex' }) } });

  const result = await run(root, ['--local']);

  assert.equal(result.status, 0, result.stderr);
  const driverOf = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.driver]));
  assert.deepEqual(driverOf, {
    assess: 'codex', comments: 'codex', dedupe: 'codex', logic: 'claude', quality: 'claude', skeptic: 'claude',
  });
});

test('неизвестный ключ или версия конфига останавливают прогон', async () => {
  const typo = makeRepo({ config: { ...CONFIG, stagez: [] } });
  const wrongVersion = makeRepo({ config: { ...CONFIG, version: 2 } });

  for (const root of [typo, wrongVersion]) {
    const result = await run(root, ['--local']);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /\.llamer\.json: (неизвестные ключи: stagez|version: поддерживается 1)/);
    assert.equal(result.launches.length, 0);
  }
});

test('своих изменений нет — этапы не поднимаются; изменения задачи не вычислились — срыв без этапов', async () => {
  const empty = makeRepo();
  git(empty, 'checkout', '-q', '-f', 'main');
  git(empty, 'clean', '-qfd', '--', 'src');
  git(empty, 'checkout', '-q', '-b', 'feature/empty');

  const nothing = await run(empty, ['--local']);
  assert.equal(nothing.status, 0, nothing.stderr);
  assert.equal(JSON.parse(nothing.stdout).findings.length, 0);
  assert.match(readFileSync(JSON.parse(nothing.stdout).report, 'utf-8'), /Своих изменений нет/);
  assert.equal(nothing.launches.length, 0);

  const broken = await run(makeRepo({ config: { ...CONFIG, sharedBranches: ['release'] } }), ['--local']);
  assert.equal(broken.status, 1);
  const answer = JSON.parse(broken.stdout);
  assert.equal(answer.ok, false);
  assert.equal(answer.failure.reason, 'scope');
  assert.match(answer.failure.detail, /ветки release нет/);
  assert.equal(broken.launches.length, 0);
});
