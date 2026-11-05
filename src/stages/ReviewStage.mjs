import { spawnStage } from '../agent/spawnStage.mjs';
import { StageTelemetry } from '../agent/StageTelemetry.mjs';
import { PromptTemplate } from './PromptTemplate.mjs';
import { describeCommands } from '../harness/program.mjs';
import { BRIEF_ENV } from '../harness/StageBrief.mjs';
import { ASSESSED } from '../config.mjs';
import { log } from '../log.mjs';

// Пять команд записи находок нужны одинаково всем, кто их пишет.
export const FINDING_COMMANDS = ['finding', 'amend', 'batch', 'list', 'blame'];

/**
 * Этап прогона: свой промпт, уровень и набор команд харнесса; драйвер — тот,
 * которым идёт уровень. Общаются этапы только журналом прогона.
 */
export class ReviewStage {
  #toolkit; #journal; #record = null; #template = null; #responding; #markResponding;

  constructor(toolkit) {
    this.#toolkit = toolkit;
    this.#responding = new Promise((resolve) => { this.#markResponding = resolve; });
  }

  get toolkit() { return this.#toolkit; }

  /**
   * Сбывается, когда модель начала отвечать этапу — с этого момента его промпт
   * лежит в кэше, — или когда этап закончился, не дойдя до ответа: ждущий не
   * должен висеть.
   */
  get responding() { return this.#responding; }

  // Журнал открывается по первому обращению: ключ разбирающего этапа задаёт
  // его конструктор, и к работе конструктора базы он ещё не известен.
  get journal() {
    this.#journal ??= this.#toolkit.artifacts.openJournal(this.key);
    return this.#journal;
  }

  /** Запись отработавшего этапа (стоимость, токены); null у не поднимавшегося. */
  get record() { return this.#record; }

  state() { return this.journal.state(); }

  // Контракт наследника. Бросающий геттер — осознанно: этап без ключа не
  // собирается, и падение при конструировании лучше тихого '' в промпте.
  get key() { throw new Error(`${this.constructor.name}: ключ этапа не объявлен`); }

  get name() { return this.key; }

  get templateKey() { return this.key; }

  get settings() { return this.#toolkit.config.stages[this.key]; }

  /** Номер уровня или ASSESSED — глубина от оценщика. */
  get level() { return this.settings.level; }

  get skills() { return this.settings.skills; }

  get allow() { return [...new Set([...this.#toolkit.config.allow, ...this.settings.allow])]; }

  get projectMemory() { return false; }

  get commands() { return ['stop']; }

  values() { throw new Error(`${this.constructor.name}: значения шаблона не объявлены`); }

  applies() { return true; }

  skipReason() { return ''; }

  get template() {
    this.#template ??= new PromptTemplate(this.templateKey);
    return this.#template;
  }

  prompt(driver, journal = null) {
    const { protocol } = this.#toolkit;
    const context = { materials: protocol.materials, journal };
    return this.template.render({ ...this.#shared(driver), ...this.values(context) });
  }

  #shared(driver) {
    const { protocol, library, commands } = this.#toolkit;
    const wanted = this.template.names;
    const shared = {
      PROTOCOL: protocol.protocol(),
      HARNESS: protocol.harness(),
      COMMANDS: describeCommands(commands, this.commands).trimEnd(),
      SKILLS: driver.skillsBlock(library, this.skills),
    };
    if (wanted.has('DIFF_ORIGIN')) shared.DIFF_ORIGIN = protocol.diffOrigin();
    if (wanted.has('PLAN')) shared.PLAN = protocol.plan(this.allow);
    return shared;
  }

  /**
   * Общая часть пакета идёт этапу, чей шаблон берёт пакет: этапу без него она
   * добавила бы чтение пакета на каждом ходу.
   */
  get sharesPack() { return this.template.names.has('PACK'); }

  async run(options = {}) {
    try {
      return await this.#run(options);
    } finally {
      this.#markResponding();
    }
  }

  async #run({ assessed = null, journal = null }) {
    const { materials, artifacts, shared } = this.#toolkit;
    if (!this.applies(materials, journal)) {
      log(`этап «${this.name}» не поднимается: ${this.skipReason()}`);
      return this.state();
    }

    const briefPath = artifacts.saveBrief(this.key, this.brief(materials));
    const level = this.level === ASSESSED ? assessed : this.level;
    const settings = this.#toolkit.config.levels[level - 1];
    const driver = this.#toolkit.drivers.get(settings.driver);
    const prompt = this.prompt(driver, journal);
    const promptPath = artifacts.savePrompt(this.name, prompt);
    const launch = driver.launch({
      stage: this.key,
      level: settings,
      projectMemory: this.projectMemory,
      commands: this.commands,
      allow: this.allow,
      shared: this.sharesPack ? shared : null,
      prompt,
      env: { [BRIEF_ENV]: briefPath },
    });
    log(`этап «${this.name}» пошёл: ${driver.name}, уровень ${level} (${launch.model}/${launch.effort}),`
      + ` промпт ${prompt.length} символов → ${promptPath}`);
    log(`этап «${this.name}» запускается: ${launch.command} ${launch.argv.join(' ')}`);

    const telemetry = new StageTelemetry({
      stage: this,
      artifacts,
      skills: this.skills.always,
      cwd: materials.cwd,
      onResponse: this.#markResponding,
    });
    const record = await spawnStage({
      name: this.name, launch, cwd: materials.cwd, telemetry,
    });

    this.#record = this.#judged(telemetry.finish(record));
    if (!this.#record.ok) {
      this.journal.recordFailure({
        stage: this.key, reason: this.#record.reason, rateLimit: this.#record.rateLimit,
      });
    }
    return this.state();
  }

  /**
   * Процесс этапа мог выйти чисто, потеряв работу: вызов харнесса не дошёл до
   * журнала или разбор не закрыт сводкой. Такой этап — сорванный, как упавший.
   */
  #judged(record) {
    if (!record.ok) return record;
    const state = this.state();
    let reason = null;
    if (record.lostWrites > 0) reason = `вызовов харнесса не дошло до журнала: ${record.lostWrites}`;
    else if (this.commands.includes('summary') && state.summaries.length === 0 && state.stopped.length === 0) {
      reason = 'сводка не записана';
    }
    if (!reason) return record;
    log(`этап «${this.name}» сорван: ${reason}`);
    return { ...record, ok: false, reason };
  }

  // diffPath — изменения задачи: находка на чужой строке отбивается сразу,
  // а не уезжает в реквест замечанием к коду, которого автор не писал.
  brief(materials) {
    return {
      stage: this.key,
      name: this.name,
      journalDir: this.journal.dir,
      baseSha: materials.baseSha,
      diffPath: materials.diffPath,
      commands: this.commands,
    };
  }

  toString() { return this.name; }
}
