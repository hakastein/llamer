import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  existsSync, readdirSync, readFileSync, utimesSync,
} from 'node:fs';
import { join } from 'node:path';
import { parse } from 'yaml';
import {
  CONFIG, SECRETS, WORKTREE, git, levels, makeRepo, run, stages, write,
} from './fixtures/repo.mjs';
import { readRunState } from '../src/domain/journal.mjs';

const stagesOf = (launches) => launches.map((launch) => launch.stage).sort();

test('--head worktree: конвейер на заглушке claude доходит до report.md и YAML-результата в stdout', async () => {
  const root = makeRepo();
  const statusBefore = git(root, 'status', '--porcelain');

  const result = await run(root, WORKTREE);

  assert.equal(result.status, 0, result.stderr);
  const answer = parse(result.stdout);
  assert.equal(answer.version, 3);
  assert.equal(answer.ok, true);
  assert.equal(answer.input, 'revisions');
  assert.equal(answer.assessment.level, 2);
  // Два разбирающих этапа отметили одно место — дедупликация оставила одну находку.
  assert.equal(answer.findings.length, 1);
  assert.deepEqual(answer.summaries.map((summary) => summary.stage).sort(), ['logic']);
  assert.match(readFileSync(answer.report, 'utf-8'), /## Находки: 1/);
  // Тот же документ — в result.yaml; многострочный текст — блоком, находка — с местом и стороной диффа.
  assert.equal(readFileSync(answer.result, 'utf-8'), result.stdout);
  assert.match(result.stdout, /^ {4}text: \|-?\n {6}заглушка этапа \S+ отметила src\/Fresh\.php:1\.\n {6}Вторая строка: токен/m);
  assert.deepEqual(Object.keys(answer), ['version', 'input', 'head', 'ok', 'base', 'assessment', 'findings', 'summaries',
    'resolvedThreads', 'gaps', 'cost', 'limits', 'stages', 'report', 'result', 'artifacts']);
  const { file, line, deleted, severity } = answer.findings[0];
  assert.deepEqual({ file, line, deleted, severity }, { file: 'src/Fresh.php', line: 1, deleted: false, severity: 'major' });
  assert.equal(answer.stages.length, result.launches.length, 'запись на каждый поднятый этап');
  assert.match(answer.stages.find((stage) => stage.name === 'logic').resume, /claude -p --resume stub-logic/);

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

test('--head worktree --exclude: от точки расхождения до рабочего дерева, без чужих правок исключённой ветки', async () => {
  const root = makeRepo();

  const { stdout } = await run(root, WORKTREE);

  const diff = readFileSync(join(parse(stdout).artifacts, 'task.diff'), 'utf-8');
  assert.match(diff, /\+\s+return 3;/, 'незакоммиченная правка в разборе');
  assert.match(diff, /b\/src\/Fresh\.php/, 'неотслеживаемый файл в разборе');
  assert.doesNotMatch(diff, /Foreign\.php/, 'правка целевой ветки после ответвления не в разборе');
});

test('опровергнутая скептиком находка с токеном пишется в лог вымаранной', async () => {
  const root = makeRepo({ config: { ...CONFIG, drivers: { claude: { env: { STUB_SKEPTIC_REFUTES: '1' } } } } });

  const { stdout, status, stderr } = await run(root, WORKTREE);

  assert.equal(status, 0, stderr);
  assert.equal(parse(stdout).findings.length, 0);
  assert.match(stderr, /скептик опроверг находок: 1/);
  assert.match(stderr, /токен \*\*\*REDACTED\*\*\*/);
  assert.ok(!stderr.includes(SECRETS.GITLAB_TOKEN), 'токен в stderr');
});

test('--head worktree: правка той же длины в секунду записи индекса попадает в снимок (racy git)', async () => {
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

  const { stdout, status, stderr } = await run(root, WORKTREE);

  assert.equal(status, 0, stderr);
  assert.match(readFileSync(join(parse(stdout).artifacts, 'task.diff'), 'utf-8'), /\+\s+return 5;/);
});

test('--head index: разбирается индекс, индекс человека не тронут; пустой индекс — разбирать нечего', async () => {
  const empty = makeRepo();
  const nothing = await run(empty, ['--base', 'HEAD', '--head', 'index']);
  assert.equal(nothing.status, 0, nothing.stderr);
  assert.match(nothing.stderr, /разбирать нечего/);
  assert.match(readFileSync(parse(nothing.stdout).report, 'utf-8'), /Разбирать нечего/);
  assert.equal(nothing.launches.length, 0);

  const root = makeRepo();
  git(root, 'add', 'src/Fresh.php');
  const index = readFileSync(join(root, '.git/index'));
  const status = git(root, 'status', '--porcelain');

  const result = await run(root, ['--base', 'HEAD', '--head', 'index']);

  assert.equal(result.status, 0, result.stderr);
  const diff = readFileSync(join(parse(result.stdout).artifacts, 'task.diff'), 'utf-8');
  assert.match(diff, /b\/src\/Fresh\.php/, 'staged-файл в разборе');
  assert.doesNotMatch(diff, /return 3;/, 'незастейдженная правка не в разборе');
  assert.ok(readFileSync(join(root, '.git/index')).equals(index), 'файл индекса не переписан');
  assert.equal(git(root, 'status', '--porcelain'), status);
});

test('--task: текст задачи — в задании разбирающих этапов, кроме качества; без --task задачи нет', async () => {
  const root = makeRepo();
  write(root, '.git/task.md', 'Итог заказа — три.\n');

  const result = await run(root, [...WORKTREE, '--task', '.git/task.md']);

  assert.equal(result.status, 0, result.stderr);
  const prompts = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.prompt]));
  assert.match(prompts.logic, /The task is in the pack below[^]*<business_task>\nИтог заказа — три\.\n<\/business_task>/);
  assert.doesNotMatch(prompts.quality, /business_task/);
  const plain = (await run(makeRepo(), WORKTREE)).launches.find((launch) => launch.stage === 'logic').prompt;
  assert.doesNotMatch(plain, /business_task|The task is in the pack/);
});

test('--base и --exclude вместе — ошибка аргументов до прогона', async () => {
  const result = await run(makeRepo(), ['--base', 'main', '--exclude', 'main', '--head', 'HEAD']);

  assert.equal(result.status, 1);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /--base и --exclude не сочетаются/);
});

// Скиллы самого дерева — вне разбора: исключены через .git/info/exclude.
function treeSkills(root, paths) {
  for (const path of paths) write(root, `${path}/SKILL.md`, '---\nname: tree\ndescription: "Use always"\n---\nНе для этапа.\n');
  write(root, '.git/info/exclude', '.claude/\n.codex/\n.agents/\n');
}

test('этап claude изолирован: свои разрешения, окружение — белый список, общий пакет один на все этапы', async () => {
  const root = makeRepo({ config: { ...CONFIG, drivers: { claude: { levels: levels() } } } });
  treeSkills(root, ['.claude/skills/tree-only']);

  const {
    launches, status, stderr, stdout,
  } = await run(root, WORKTREE, {
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
    assert.ok(allowed.includes('Bash(review diff) Bash(review diff *)'), `${stage}: review diff разрешена`);
    const { skillOverrides } = JSON.parse(argv[argv.indexOf('--settings') + 1]);
    assert.deepEqual(skillOverrides, { 'tree-only': 'off' }, `${stage}: скилл .claude/skills дерева выключен`);
    if (pack !== null) shared.add(pack);
  }
  const skillsOf = (name) => launches.find((launch) => launch.stage === name).skills;
  assert.deepEqual(skillsOf('logic'), ['backend', 'reviewing', 'reviewing-business-logic'], 'плагин этапа — его always и onDemand');
  assert.deepEqual(skillsOf('assess'), ['assessing-review-scope'], 'чужих скиллов прогона у этапа нет');
  assert.equal(shared.size, 1, 'общая часть пакета разошлась между этапами — кэш не поделится');
  assert.match([...shared][0], /<request_materials>/);
  // Голова локального прогона — снимок рабочего дерева: незакоммиченная правка в выводе, как в пакете.
  const { diff } = launches.find((launch) => launch.stage === 'logic');
  assert.match(diff, /^diff --git a\/src\/Order\.php b\/src\/Order\.php\n[^]*\n {4}4 \+    return 3;\n/);
  assert.ok([...shared][0].includes(diff.trimEnd()), 'review diff — та же секция, что в пакете');

  const byStage = Object.fromEntries(launches.map((launch) => [launch.stage, launch.argv]));
  const modelOf = (argv) => `${argv[argv.indexOf('--model') + 1]}/${argv[argv.indexOf('--effort') + 1]}`;
  assert.equal(modelOf(byStage.assess), 'model-1/effort-1', 'оценщик — уровень 1');
  assert.equal(modelOf(byStage.logic), 'model-2/effort-2', 'logic идёт на уровне оценщика (2)');
  assert.ok(byStage.logic.includes('--append-system-prompt-file'));
  assert.ok(!byStage.dedupe.includes('--append-system-prompt-file'), 'дедупликации пакет не нужен');

  const logic = launches.find((launch) => launch.stage === 'logic').prompt;
  assert.match(logic, /- stage:reviewing-business-logic\n- stage:reviewing\n/, 'always: встроенный и добавленный конфигом');
  assert.doesNotMatch(logic, /backend/, 'onDemand в задании не называется: claude видит скиллы плагина сам');

  // Сессия этапа — в артефактах без кредов и с вымаранным токеном; команда продолжения — в отчёте.
  const answer = parse(stdout);
  const session = join(answer.artifacts, 'sessions', 'logic');
  assert.deepEqual(readdirSync(session, { recursive: true }).sort(), ['projects', 'projects/-stub-tree', 'projects/-stub-tree/stub-logic.jsonl']);
  assert.match(readFileSync(join(session, 'projects/-stub-tree/stub-logic.jsonl'), 'utf-8'), /токен \*\*\*REDACTED\*\*\*/);
  const resume = `CLAUDE_CONFIG_DIR='${session}' claude -p --resume stub-logic --model 'model-2' --effort 'effort-2' '<вопрос>'`;
  assert.ok(readFileSync(answer.report, 'utf-8').includes(`- logic: \`${resume}\``), 'команда продолжения в report.md');
  assert.ok(stderr.includes(`продолжить сессию «logic» вопросом: ${resume}`));
});

test('драйвер codex: read-only, временные HOME и CODEX_HOME, журнал пишет правилом', async () => {
  const root = makeRepo({
    config: {
      ...CONFIG,
      drivers: { codex: { levels: levels().map((level) => ({ ...level, model: 'gpt-6.1-terra' })) } },
      stages: Object.fromEntries(Object.entries(CONFIG.stages).map(([key, stage]) => [key, { ...stage, drivers: ['codex'] }])),
    },
  });
  treeSkills(root, ['.codex/skills/tree-codex', '.agents/skills/group/tree-agents']);

  const result = await run(root, WORKTREE);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(parse(result.stdout).findings.length, 1);
  assert.deepEqual(stagesOf(result.launches), ['assess', 'comments', 'dedupe', 'logic', 'quality', 'skeptic']);
  for (const launch of result.launches) {
    assert.equal(launch.driver, 'codex');
    assert.deepEqual(launch.argv.slice(0, 4), ['exec', '--json', '--sandbox', 'read-only'], launch.stage);
    assert.ok(!launch.argv.includes('--ignore-user-config'), `${launch.stage}: config.toml этапа — свой, его надо читать`);
    assert.ok(launch.argv.includes('skills.bundled.enabled=false'), `${launch.stage}: встроенные скиллы codex отключены`);
    assert.match(launch.config.split('\n[[')[0], /^allow_login_shell = false$/m, `${launch.stage}: команды — без login-шелла, до таблиц config.toml`);
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
  const dedupe = result.launches.find((launch) => launch.stage === 'dedupe');
  assert.doesNotMatch(dedupe.config, /developer_instructions/, 'дедупликации пакет не нужен');
  for (const path of ['.codex/skills/tree-codex', '.agents/skills/group/tree-agents']) {
    assert.ok(dedupe.config.includes(`[[skills.config]]\npath = "${root}/${path}/SKILL.md"\nenabled = false\n`), `${path}: скилл дерева выключен`);
  }
  assert.deepEqual(result.launches.find((launch) => launch.stage === 'assess').skills, ['assessing-review-scope'], 'чужих скиллов прогона у этапа нет');
  assert.match(logic.prompt, /closed as `values\['"'"'severity'"'"'\]`/, 'кавычка — в форме, которую признают правила codex');
  assert.match(logic.prompt, /- \$reviewing-business-logic\n- \$reviewing\n/, 'always — упоминанием $имя, оно подключает скилл');
  assert.doesNotMatch(logic.prompt, /backend/, 'onDemand в задании не называется: упоминание подключило бы его без выбора');
  for (const name of ['reviewing', 'backend']) {
    assert.ok(logic.skills?.includes(name), `${name}: codex не найдёт скилл в $HOME/.agents/skills`);
  }

  // Цена и пик контекста — по запросам из журнала сессии заглушки: 2000 и 3000 токенов входа.
  const { artifacts, report } = parse(result.stdout);
  const cost = JSON.parse(readFileSync(join(artifacts, 'cost.json'), 'utf-8'));
  for (const stage of cost.stages) {
    assert.equal(stage.contextTokens, 3000, stage.name);
    assert.ok(Math.abs(stage.cost - 0.0027) < 1e-9, `${stage.name}: ${stage.cost}`);
    assert.deepEqual(stage.requests.map((usage) => [usage.input_tokens, usage.cached_input_tokens, usage.output_tokens, usage.reasoning_output_tokens]),
      [[2000, 1500, 100, undefined], [3000, 2500, 200, 50]], `${stage.name}: запросы из журнала сессии`);
  }

  // Журнал сессии этапа — в артефактах: без auth.json, с вымаранным токеном; продолжение — от обычного входа.
  const session = join(artifacts, 'sessions', 'logic');
  assert.deepEqual(readdirSync(session).sort(), ['config.toml', 'rules', 'sessions']);
  assert.match(readFileSync(join(session, 'sessions/2026/11/05/rollout-stub.jsonl'), 'utf-8'), /токен \*\*\*REDACTED\*\*\*/);
  const auth = join(result.env.CODEX_HOME, 'auth.json');
  const resume = `ln -sf '${auth}' '${session}/auth.json' && CODEX_HOME='${session}' codex exec resume --skip-git-repo-check`
    + ` -c 'model="gpt-6.1-terra"' -c 'model_reasoning_effort="effort-2"' stub-logic '<вопрос>'`;
  assert.ok(readFileSync(report, 'utf-8').includes(`- logic: \`${resume}\``), 'команда продолжения в report.md');
});


test('этап вышел без сводки или с отклонённым вызовом харнесса — сорван: код 1, причина в отчёте', async () => {
  for (const [knob, stage, reason] of [
    ['STUB_NO_SUMMARY', 'logic', 'сводка не записана'],
    ['STUB_DENY_STAGE', 'quality', 'вызовов харнесса не дошло до журнала: 1'],
  ]) {
    const root = makeRepo({ config: { ...CONFIG, drivers: { claude: { env: { [knob]: stage } } } } });

    const result = await run(root, WORKTREE);

    assert.equal(result.status, 1, knob);
    const answer = parse(result.stdout);
    assert.ok(answer.gaps.includes(`этап «${stage}» сорвался: ${reason}`), `${knob}: ${answer.gaps}`);
    const report = readFileSync(answer.report, 'utf-8');
    assert.match(report, new RegExp(`## Недостача\\n\\n- этап «${stage}» сорвался: ${reason}`), knob);
    assert.match(report, new RegExp(`${stage} \\(${reason}\\)`), `${knob}: этап помечен в таблице цены`);
  }
});

test('--base/--head ревизии: голова выкладывается отдельным деревом и убирается после прогона', async () => {
  const root = makeRepo();
  git(root, 'add', '-A');
  git(root, 'commit', '-q', '-m', 'finish');
  const head = git(root, 'rev-parse', 'HEAD');
  const base = git(root, 'merge-base', 'main', 'HEAD');
  git(root, 'checkout', '-q', 'main');

  const result = await run(root, ['--base', base, '--head', head]);

  assert.equal(result.status, 0, result.stderr);
  const answer = parse(result.stdout);
  assert.equal(answer.input, 'revisions');
  assert.equal(answer.base, base);
  assert.deepEqual(answer.findings.map((finding) => finding.file), ['src/Fresh.php']);
  assert.match(result.stderr, /выложена в/);
  assert.equal(git(root, 'worktree', 'list').split('\n').length, 1, 'временное дерево убрано');
  assert.equal(git(root, 'rev-parse', '--abbrev-ref', 'HEAD'), 'main');
});

test('--preflight: пробелы окружения YAML-ом, без флага прогон не стартует', async () => {
  const config = { ...CONFIG };
  config.stages = { ...CONFIG.stages, extra: { skills: { always: ['missing-skill'] }, task: 'Что-то ещё.', drivers: ['claude'] } };
  const root = makeRepo({ config });

  const preflight = await run(root, [...WORKTREE, '--preflight']);
  assert.equal(preflight.status, 1);
  const answer = parse(preflight.stdout);
  assert.equal(answer.ok, false);
  assert.deepEqual(answer.gaps.map((gap) => gap.id), ['skills']);

  const refused = await run(root, WORKTREE);
  assert.equal(refused.status, 1);
  assert.match(refused.stderr, /окружение не готово/);
  assert.equal(refused.launches.length, 0);
  assert.equal(existsSync(join(root, '.llamer')), false, 'до проверки окружения артефактов нет');
});

test('preflight проверяет драйверы всех этапов: неработающий бинарь и отсутствие входа', async () => {
  const drivers = { codex: { bin: 'codex-нет-такого' } };
  const gapsOf = async (config) => parse((await run(makeRepo({ config }), [...WORKTREE, '--preflight'], { CLAUDE_CODE_OAUTH_TOKEN: '' })).stdout)
    .gaps.map((gap) => gap.id).sort();

  assert.deepEqual(await gapsOf({ ...CONFIG, drivers }), ['auth-claude'], 'codex нет ни у одного этапа — не проверяется');
  // Этап тестов в этом прогоне не поднимется (файлов под маски нет), но его драйвер проверяется до прогона.
  const tests = { ...CONFIG, drivers, stages: stages(CONFIG.stages, { tests: ['claude', 'codex'] }) };
  assert.deepEqual(await gapsOf(tests), ['auth-claude', 'driver-codex']);
});

test('конфиг из версии и driver: встроенные этапы идут со встроенными скиллами', async () => {
  const root = makeRepo({ config: { version: 2, driver: 'claude' } });

  const preflight = await run(root, [...WORKTREE, '--preflight']);
  assert.equal(preflight.status, 0, preflight.stderr);
  assert.deepEqual(parse(preflight.stdout).gaps, []);

  const result = await run(root, WORKTREE);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(stagesOf(result.launches), ['assess', 'comments', 'dedupe', 'logic', 'quality', 'skeptic']);
  const prompts = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.prompt]));
  assert.match(prompts.assess, /- stage:assessing-review-scope/);
  assert.match(prompts.logic, /- stage:reviewing-business-logic/);
  assert.match(prompts.quality, /- stage:reviewing-code-quality/);
  assert.match(prompts.skeptic, /- stage:refuting-review-findings/);
  assert.match(prompts.skeptic, /written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer and the test reviewer\./);
  const plugin = join(parse(result.stdout).artifacts, 'stage-plugin', 'skills');
  assert.deepEqual(readdirSync(plugin).sort(), ['assessing-review-scope', 'refuting-review-findings', 'reviewing-business-logic', 'reviewing-code-quality']);
});

test('справки команд в задании нет — одна строка о `review --help`, а он даёт справку каждой команды этапа', async () => {
  const result = await run(makeRepo(), WORKTREE);

  assert.equal(result.status, 0, result.stderr);
  for (const { stage, prompt, commands, help } of result.launches) {
    assert.match(prompt, /## Your Commands\n\nThe recording command is `review`; its commands and their formats are in `review --help`\.\n\n## /, stage);
    assert.doesNotMatch(prompt, /Usage: review|\$ review/, stage);
    for (const name of commands) {
      assert.match(help, new RegExp(`\\n  ${name}  [^\\n]+\\n`), `${stage}: ${name} в перечне`);
      assert.match(help, new RegExp(`\\n\\\$ review ${name} --help\\nUsage: review ${name} \\[options\\][^\\n]*\\n[^]*?\\nOptions:\\n`), `${stage}: справка ${name}`);
    }
  }
});

test('встроенный разбирающий этап идёт своим шаблоном старого движка, свой этап — общим с его task', async () => {
  const root = makeRepo();
  write(root, 'docs/guide.md', 'Руководство.\n');
  write(root, 'tests/OrderTest.php', '<?php\n\n// Проверяет сумму заказа.\nfunction testTotal() {}\n');

  const result = await run(root, WORKTREE);

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
  const helps = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.help]));
  const summary = /\$ review summary --help/;
  for (const stage of ['logic', 'docs']) assert.match(helps[stage], summary, stage);
  for (const stage of ['quality', 'comments', 'tests']) assert.doesNotMatch(helps[stage], summary, stage);
  assert.match(prompts.skeptic, /written by different stages: the business-logic reviewer, the code-quality reviewer, the comments reviewer, the test reviewer and the docs stage\./);
});

test('prompt конфига: строка протокола — в «How This Run Works», план заменяет поиск по тексту; без него — поиск', async () => {
  const config = {
    ...CONFIG,
    prompt: { protocol: '`nav` is up.', plan: 'Plan with `nav def`.' },
    stages: { ...CONFIG.stages, quality: { ...CONFIG.stages.quality, prompt: { plan: 'Quality plan.' } } },
  };
  const prompts = Object.fromEntries((await run(makeRepo({ config }), WORKTREE)).launches
    .map((launch) => [launch.stage, launch.prompt]));
  const plain = Object.fromEntries((await run(makeRepo(), WORKTREE)).launches.map((launch) => [launch.stage, launch.prompt]));

  for (const stage of ['assess', 'logic', 'skeptic']) {
    assert.match(prompts[stage], /such files open by direct path\.\n`nav` is up\.\nLinters, tests/, stage);
  }
  assert.match(prompts.logic, /## Your Plan\n\nPlan with `nav def`\.\n\nThen work through the plan\./);
  assert.match(prompts.quality, /## Your Plan\n\nQuality plan\.\n\nThen work through the plan\./);
  assert.doesNotMatch(prompts.logic, /or a text search/);
  assert.match(plain.logic, /direct path\.\nLinters, tests/);
  assert.match(plain.logic, /## Your Plan\n\nPresent a plan before your first search: [^\n]*, or a text search\./);
});

test('этап на двух драйверах — два экземпляра, каждый на уровне своей лестницы, находки помечены драйвером', async () => {
  // logic — на уровне оценщика (2), quality — на 2, comments — на 1; оценщик и дедупликация — codex, скептик — claude.
  const config = {
    ...CONFIG,
    drivers: { claude: { levels: levels('claude') }, codex: { levels: levels('codex') } },
    stages: stages(CONFIG.stages, {
      assess: ['codex'], comments: ['codex'], dedupe: ['codex'], logic: ['claude', 'codex'], quality: ['codex', 'claude'],
      skeptic: ['claude'],
    }),
  };

  const result = await run(makeRepo({ config }), WORKTREE);

  assert.equal(result.status, 0, result.stderr);
  const after = (argv, flag) => argv[argv.indexOf(flag) + 1];
  const modelOf = ({ driver, argv }) => (driver === 'codex' ? after(argv, '-m') : `${after(argv, '--model')}/${after(argv, '--effort')}`);
  const launched = Object.fromEntries(result.launches.map((launch) => [launch.stage, `${launch.driver} ${modelOf(launch)}`]));
  assert.deepEqual(launched, {
    assess: 'codex codex-1',
    comments: 'codex codex-1',
    dedupe: 'codex codex-1',
    'logic·claude': 'claude claude-2/effort-2',
    'logic·codex': 'codex codex-2',
    'quality·claude': 'claude claude-2/effort-2',
    'quality·codex': 'codex codex-2',
    skeptic: 'claude claude-2/effort-2',
  });
  assert.match(result.stderr, /этап «logic·codex» пошёл: codex, уровень 2 \(codex-2\/effort-2\)/);

  const answer = parse(result.stdout);
  const briefOf = (name) => JSON.parse(readFileSync(join(answer.artifacts, 'briefs', `${name}.json`), 'utf-8'));
  assert.deepEqual(briefOf('quality·codex').siblings, ['quality·claude'], 'экземпляр скрывает находки соседа по этапу');
  assert.deepEqual(briefOf('skeptic').siblings, []);
  const written = readRunState(join(answer.artifacts, 'journal')).findings.map((finding) => `${finding.stage} ${finding.driver}`);
  assert.deepEqual(written.sort(), [
    'comments codex', 'logic·claude claude', 'logic·codex codex', 'quality·claude claude', 'quality·codex codex',
  ]);
  assert.deepEqual(answer.findings.map((finding) => Boolean(finding.driver)), [true], 'находка в отчёте — с драйвером');
  assert.deepEqual(answer.summaries.map((summary) => summary.stage).sort(), ['logic·claude', 'logic·codex']);
  const cost = JSON.parse(readFileSync(join(answer.artifacts, 'cost.json'), 'utf-8'));
  assert.deepEqual(cost.stages.map((stage) => stage.name).filter((name) => name.includes('·')).sort(),
    ['logic·claude', 'logic·codex', 'quality·claude', 'quality·codex']);
  // Лимиты подписки — по драйверу: claude из rate_limit_event потока, codex из журнала сессии.
  assert.deepEqual(answer.limits, {
    codex: { status: 'allowed', reason: null, windows: { seven_day: { usedPercent: 70, resetsAt: 1794213888 } } },
    claude: {
      status: 'allowed',
      reason: null,
      windows: { five_hour: { usedPercent: 45, resetsAt: 1794480600 }, seven_day: { usedPercent: 58, resetsAt: 1794787200 } },
    },
  });
  assert.match(result.stderr, /лимиты codex: неделя — использовано 70%, осталось 30%, сброс 2026-11-09 08:44 UTC\n/);
  assert.match(result.stderr, /лимиты claude: 5 часов — использовано 45%, осталось 55%, сброс 2026-11-12 10:50 UTC; неделя — /);

  const prompts = Object.fromEntries(result.launches.map((launch) => [launch.stage, launch.prompt]));
  const listedStages = 'the business-logic reviewer (logic·claude), the business-logic reviewer (logic·codex),'
    + ' the code-quality reviewer (quality·codex), the code-quality reviewer (quality·claude), the comments reviewer,'
    + ' the test reviewer and the docs stage';
  assert.ok(prompts.skeptic.includes(`written by different stages: ${listedStages}.`), 'скептик называет экземпляры');
  assert.ok(prompts.dedupe.includes(`findings from ${listedStages}.`), 'дедупликация называет экземпляры');
  for (const name of ['logic·claude', 'logic·codex']) {
    assert.match(prompts[name], /^You are a Senior Software Engineer with expertise in domain logic/, `${name}: задание этапа общее`);
  }
});

test('неизвестный ключ или версия конфига останавливают прогон', async () => {
  const typo = makeRepo({ config: { ...CONFIG, stagez: [] } });
  const wrongVersion = makeRepo({ config: { ...CONFIG, version: 1 } });

  for (const root of [typo, wrongVersion]) {
    const result = await run(root, WORKTREE);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /\.llamer\.json: (неизвестные ключи: stagez|version: поддерживается 2)/);
    assert.equal(result.launches.length, 0);
  }
});

test('разбирать нечего — этапы не поднимаются; изменения задачи не вычислились — срыв без этапов', async () => {
  const empty = makeRepo();
  git(empty, 'checkout', '-q', '-f', 'main');
  git(empty, 'clean', '-qfd', '--', 'src');
  git(empty, 'checkout', '-q', '-b', 'feature/empty');

  const nothing = await run(empty, WORKTREE);
  assert.equal(nothing.status, 0, nothing.stderr);
  assert.equal(parse(nothing.stdout).findings.length, 0);
  assert.match(readFileSync(parse(nothing.stdout).report, 'utf-8'), /Разбирать нечего/);
  assert.equal(nothing.launches.length, 0);

  // Вторая исключённая ревизия — без общей истории с головой: открыть вход это не мешает, вычислить изменения — да.
  const lonely = makeRepo();
  git(lonely, 'checkout', '-q', '--orphan', 'lonely');
  git(lonely, 'commit', '-q', '--allow-empty', '-m', 'сирота');
  git(lonely, 'checkout', '-q', 'feature/dev-7-orders');
  const broken = await run(lonely, [...WORKTREE, '--exclude', 'lonely']);
  assert.equal(broken.status, 1);
  const answer = parse(broken.stdout);
  assert.equal(answer.ok, false);
  assert.equal(answer.failure.reason, 'scope');
  assert.match(answer.failure.detail, /и lonely нет общей истории/);
  assert.equal(broken.launches.length, 0);
});

test('оценщик сорвался — прогон не удался, а лимиты подписки всё равно в результате и в логе', async () => {
  const result = await run(makeRepo({ config: { ...CONFIG, drivers: { claude: { env: { STUB_FAIL_STAGE: 'assess' } } } } }), WORKTREE);

  assert.equal(result.status, 1, result.stderr);
  const answer = parse(result.stdout);
  assert.equal(answer.ok, false);
  assert.deepEqual(answer.limits.claude.windows.seven_day, { usedPercent: 58, resetsAt: 1794787200 });
  assert.match(result.stderr, /лимиты claude: 5 часов — использовано 45%/);
});
