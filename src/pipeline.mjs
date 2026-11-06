import {
  AssessStage, ThreadsStage, AnalysisStage, DedupeStage, SkepticStage,
} from './stages/stages.mjs';
import { analysisStages } from './config.mjs';
import { foldJournals } from './domain/journal.mjs';
import { log } from './log.mjs';

/**
 * Протокол прогона: волна оценки (оценщик и треды) → проверка вердикта →
 * волна разбирающих этапов на глубине от оценщика, экземпляр на каждый драйвер
 * этапа → дедупликация → скептик,
 * каждому — свёртка предыдущих журналов. В каждой волне первым идёт ведущий
 * этап, остальные — с началом его ответа (`runWave`).
 */
export class ReviewPipeline {
  #toolkit;

  constructor(toolkit) {
    this.#toolkit = toolkit;
  }

  async run() {
    const toolkit = this.#toolkit;
    const assess = new AssessStage(toolkit);
    const opening = [assess, new ThreadsStage(toolkit)];
    await runWave(assess, opening);

    // Без вердикта оценщика глубина разбора неизвестна — дальше не идём.
    const assessment = assess.assessment();
    if (!assessment) {
      return RunOutcome.brokenOff({
        journal: foldStages(opening),
        failure: assess.state().failure,
        records: collectRecords(opening),
      });
    }
    log(`оценка: ${assessment.nature}, риск ${assessment.risk}, уровень ${assessment.level}`);

    const deep = { assessed: assessment.level };
    const review = analysisStages(toolkit.config)
      .flatMap((settings) => settings.drivers.map((driver) => new AnalysisStage(toolkit, settings, driver)));
    const leader = review.find((stage) => stage.applies(toolkit.materials)) ?? review[0];
    await runWave(leader, review, deep);

    const prior = [...opening, ...review];
    const dedupe = new DedupeStage(toolkit);
    await dedupe.run({ ...deep, journal: foldStages(prior) });
    const skeptic = new SkepticStage(toolkit);
    await skeptic.run({ ...deep, journal: foldStages([...prior, dedupe]) });

    const all = [...prior, dedupe, skeptic];
    return RunOutcome.completed({
      journal: foldStages(all),
      assessment,
      records: collectRecords(all),
    });
  }
}

/**
 * Волна этапов, ведущий первым. Общую часть пакета ведущий пишет в кэш, а
 * запись доступна соседним запросам с началом ответа модели: стартовав вместе с
 * ним, этапы волны записали бы её каждый сам. Ждать приходится секунды.
 */
async function runWave(leader, wave, options = {}) {
  const leading = leader.run(options);
  await leader.responding;
  const rest = wave.filter((stage) => stage !== leader);
  await Promise.all([leading, ...rest.map((stage) => stage.run(options))]);
}

function foldStages(stages) {
  return foldJournals(stages.map((stage) => stage.journal));
}

// Не поднимавшийся этап записи не оставляет.
function collectRecords(stages) {
  return stages.map((stage) => stage.record).filter(Boolean);
}

/** Итог прогона этапов — единственное, что пересекает границу pipeline → cli. */
export class RunOutcome {
  #journal; #assessment; #failure; #records;

  constructor({
    journal, assessment = null, failure = null, records = [],
  }) {
    this.#journal = journal;
    this.#assessment = assessment;
    this.#failure = failure;
    this.#records = records;
  }

  static completed({ journal, assessment, records }) {
    return new RunOutcome({ journal, assessment, records });
  }

  static brokenOff({ journal, failure, records }) {
    return new RunOutcome({ journal, failure, records });
  }

  get ok() { return this.#assessment !== null; }

  get journal() { return this.#journal; }

  /** Последний срыв волны оценки — для сообщения о неудаче. */
  get failure() { return this.#failure; }

  /** Записи отработавших этапов: стоимость, токены, длительность. */
  get records() { return [...this.#records]; }
}
