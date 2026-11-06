import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { HELP, parseArguments } from './arguments.mjs';
import { checkReadiness, formatGaps, isBlocking } from './preflight.mjs';
import { driversInUse, skillsInUse } from './config.mjs';
import {
  GitlabRequest, LocalRequest, RangeRequest, InputError, openTree,
} from './requests.mjs';
import { openRunArtifacts } from './RunArtifacts.mjs';
import { planReviewScope, gatherMaterials } from './materials/gather.mjs';
import { TaskChangesError } from './materials/taskChanges.mjs';
import { prepareStageBin } from './agent/stageBin.mjs';
import { prepareDriver } from './drivers/index.mjs';
import { RunProtocol } from './stages/RunProtocol.mjs';
import { ReviewPipeline } from './pipeline.mjs';
import { ReviewReport } from './report/ReviewReport.mjs';
import { GitlabDelivery } from './report/GitlabDelivery.mjs';
import { ReportDelivery, REPORT_VERSION } from './report/ReportDelivery.mjs';
import { collectDropped } from './report/dropped.mjs';
import { STARTED_AT, log, stamped } from './log.mjs';

function openRequest(options, root, env) {
  if (options.input === 'gitlab') return GitlabRequest.open({ ref: options.mr, env });
  if (options.input === 'local') return LocalRequest.open({ cwd: root, target: options.target });
  return RangeRequest.open({ cwd: root, base: options.base, head: options.head });
}

async function review({
  options, ready, env, artifacts, tmp,
}) {
  const { root, config, tracker, library } = ready;
  const request = await openRequest(options, root, env);
  const delivery = options.publish
    ? new GitlabDelivery({ request, runPath: artifacts.path })
    : new ReportDelivery({ request, artifacts });

  const tree = openTree({ request, root });
  try {
    let scope;
    try {
      scope = await planReviewScope({
        request, tree, config, sinceLast: options.sinceLast,
      });
    } catch (error) {
      if (!(error instanceof TaskChangesError)) throw error;
      // Дифф реквеста вместо изменений задачи — разбор чужого кода, ради отказа
      // от которого они и считаются: без них ревью не идёт вовсе.
      log(`изменения задачи не вычислены: ${error.message} — ревью не запускается`);
      await delivery.deliverFailure({ reason: 'scope', detail: error.message });
      return false;
    }
    if (scope.nothingOwn) {
      log('своих изменений нет — этапы не поднимаются');
      await delivery.deliverNothingOwn({ task: scope.task, markSha: request.headSha });
      return true;
    }

    const materials = await gatherMaterials({
      request, tree, scope, tracker, artifacts,
    });
    // Драйвер получает пакет как есть, а в артефакты ложится копия с вымаранными секретами.
    artifacts.savePrompt('общий пакет', materials.sharedPack);
    const shared = { text: materials.sharedPack, path: join(tmp, 'request-materials.md') };
    writeFileSync(shared.path, shared.text, 'utf-8');

    const skills = skillsInUse(config);
    const runSkills = skills.length > 0 ? library.materialize(join(artifacts.path, 'stage-plugin'), skills) : null;
    const stageBin = prepareStageBin(join(tmp, 'bin'));
    const drivers = new Map(driversInUse(config).map((name) => [name, prepareDriver(name, {
      settings: config.drivers[name], env, stageBin, runSkills, tmp,
    })]));

    const toolkit = {
      artifacts,
      drivers,
      materials,
      library,
      config,
      shared,
      protocol: new RunProtocol({ materials }),
    };
    const outcome = await new ReviewPipeline(toolkit).run();

    if (!outcome.ok) {
      const detail = 'оценщик не отдал вердикт: неизвестна глубина разбора';
      log(`прогон не удался: ${detail}`);
      const found = outcome.journal.survivingFindings.length;
      if (found > 0) log(`найдено до срыва: ${found} — остались в журнале прогона`);
      await delivery.deliverFailure({
        reason: outcome.failure?.reason ?? 'other', rateLimit: outcome.failure?.rateLimit ?? null, detail,
      });
      artifacts.reportCost(outcome.records, Date.now() - STARTED_AT);
      return false;
    }

    const report = new ReviewReport({
      notices: ready.gaps.map((item) => item.message),
      journal: outcome.journal,
      materials,
      stages: outcome.records,
      markSha: request.headSha,
      elapsedMs: Date.now() - STARTED_AT,
    });
    const { unpublished, failures } = await delivery.deliver(report);

    for (const line of report.repeats) log(line);
    for (const line of collectDropped(outcome.journal.findings)) log(line);
    for (const gap of report.shortfall(unpublished).gaps) log(`недостача: ${gap}`);
    artifacts.reportCost(outcome.records, Date.now() - STARTED_AT);
    // Сорванный этап — неполное ревью, даже если отчёт доставлен.
    return failures.length === 0 && outcome.journal.failures.length === 0;
  } finally {
    tree.close();
  }
}

async function main(argv, env) {
  const options = parseArguments(argv);
  if (options.help) {
    process.stdout.write(HELP);
    return true;
  }

  const ready = checkReadiness({ cwd: process.cwd(), options, env });
  const blocking = ready.gaps.filter(isBlocking);
  if (options.preflight) {
    process.stdout.write(`${JSON.stringify({ version: REPORT_VERSION, ok: blocking.length === 0, gaps: ready.gaps }, null, 2)}\n`);
    return blocking.length === 0;
  }
  if (blocking.length > 0) throw new InputError(`прогон не начат — окружение не готово:\n${formatGaps(blocking)}`);
  for (const item of ready.gaps) log(`предупреждение: ${item.message}`);
  log(`конфиг: ${ready.config.source}`);

  const artifacts = openRunArtifacts({ root: ready.root, startedAt: STARTED_AT });
  log(`артефакты прогона: ${artifacts.path}`);
  // Временное прогона: обёртка харнесса, пакет, приватные конфиги драйверов
  // с копией кредов — после прогона не должно остаться ничего.
  const tmp = mkdtempSync(join(tmpdir(), 'llamer-run-'));
  try {
    return await review({
      options, ready, env, artifacts, tmp,
    });
  } finally {
    rmSync(tmp, { recursive: true, force: true });
    artifacts.redactJournals();
    log(`артефакты прогона: ${artifacts.path}`);
  }
}

try {
  process.exitCode = (await main(process.argv.slice(2), process.env)) ? 0 : 1;
} catch (error) {
  stamped(String(error.message).split('\n').map((line) => `[llamer] ${line}`).join('\n'));
  process.exitCode = 1;
}
