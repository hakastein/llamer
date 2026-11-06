import { spawnStage } from '../agent/spawnStage.mjs';
import { StageTelemetry } from '../agent/StageTelemetry.mjs';
import { PromptTemplate } from './PromptTemplate.mjs';
import { HARNESS } from '../agent/stageBin.mjs';
import { BRIEF_ENV } from '../harness/StageBrief.mjs';
import { ASSESSED, instanceName } from '../config.mjs';
import { log } from '../log.mjs';

// Пять команд записи находок нужны одинаково всем, кто их пишет.
export const FINDING_COMMANDS = ['finding', 'amend', 'batch', 'list', 'blame'];

/**
 * Экземпляр этапа прогона на одном драйвере: свой промпт, уровень на лестнице
 * драйвера и набор команд харнесса. Служебный этап идёт единственным драйвером
 * из конфига. Общаются этапы только журналом прогона.
 */
export class ReviewStage {
  #toolkit; #driver; #journal; #record = null; #template = null; #responding; #markResponding;

  constructor(toolkit, driver = null) {
    this.#toolkit = toolkit;
    this.#driver = driver;
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
    this.#journal ??= this.#toolkit.artifacts.openJournal(this.id);
    return this.#journal;
  }

  /** Запись отработавшего этапа (стоимость, токены); null у не поднимавшегося. */
  get record() { return this.#record; }

  state() { return this.journal.state(); }

  // Контракт наследника. Бросающий геттер — осознанно: этап без ключа не
  // собирается, и падение при конструировании лучше тихого '' в промпте.
  get key() { throw new Error(`${this.constructor.name}: ключ этапа не объявлен`); }

  get driverName() { return this.#driver ?? this.settings.drivers[0]; }

  /** Имя экземпляра: ключ этапа, у этапа на нескольких драйверах — с драйвером. */
  get id() { return instanceName(this.settings, this.driverName); }

  get name() { return this.id; }

  get templateKey() { return this.key; }

  get settings() { return this.#toolkit.config.stages[this.key]; }

  /** Номер уровня или ASSESSED — глубина от оценщика. */
  get level() { return this.settings.level; }

  get skills() { return this.settings.skills; }

  get allow() { return [...new Set([...this.#toolkit.config.allow, ...this.settings.allow])]; }

  get projectMemory() { return false; }

  get relatedRequired() { return true; }

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
    const { protocol, library } = this.#toolkit;
    const wanted = this.template.names;
    const { prompt } = this.settings;
    const shared = {
      PROTOCOL: protocol.protocol(prompt.protocol),
      HARNESS: protocol.harness(driver.quote),
      COMMANDS: `The recording command is \`${HARNESS}\`; its commands and their formats are in \`${HARNESS} --help\`.`,
      SKILLS: driver.skillsBlock(library, this.skills),
    };
    if (wanted.has('DIFF_ORIGIN')) shared.DIFF_ORIGIN = protocol.diffOrigin();
    if (wanted.has('PLAN')) shared.PLAN = protocol.plan(prompt.plan);
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

    const briefPath = artifacts.saveBrief(this.id, this.brief(materials));
    const level = this.level === ASSESSED ? assessed : this.level;
    const settings = this.#toolkit.config.drivers[this.driverName].levels[level - 1];
    const driver = this.#toolkit.drivers.get(this.driverName);
    const prompt = this.prompt(driver, journal);
    const promptPath = artifacts.savePrompt(this.name, prompt);
    const launch = driver.launch({
      stage: this.id,
      level: settings,
      projectMemory: this.projectMemory,
      commands: this.commands,
      allow: this.allow,
      shared: this.sharesPack ? shared : null,
      prompt,
      env: { [BRIEF_ENV]: briefPath },
      skills: [...this.skills.always, ...this.skills.onDemand],
      cwd: materials.cwd,
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

    this.#record = this.#judged(telemetry.finish({ ...record, resume: this.#keepSession(launch, record.sessionId) }));
    if (!this.#record.ok) {
      this.journal.recordFailure({
        stage: this.id, reason: this.#record.reason, rateLimit: this.#record.rateLimit,
      });
    }
    return this.state();
  }

  // Без журнала сессии ревью не хуже: сбой копирования не роняет этап.
  #keepSession(launch, sessionId) {
    if (!sessionId || !launch.keepSession) return null;
    try {
      return launch.keepSession(this.#toolkit.artifacts.sessionDir(this.id), sessionId);
    } catch (error) {
      log(`этап «${this.name}»: журнал сессии не сохранён (${error.message})`);
      return null;
    }
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
  // siblings — экземпляры этого же этапа на других драйверах. Их находок этап не
  // видит: увидев чужие, он не записал бы своих на тех же местах. Сводит экземпляры дедупликация.
  brief(materials) {
    return {
      stage: this.id,
      driver: this.driverName,
      name: this.name,
      siblings: this.settings.drivers.filter((driver) => driver !== this.driverName)
        .map((driver) => instanceName(this.settings, driver)),
      journalDir: this.journal.dir,
      baseSha: materials.baseSha,
      diffPath: materials.diffPath,
      relatedRequired: this.relatedRequired,
      commands: this.commands,
    };
  }

  toString() { return this.name; }
}
