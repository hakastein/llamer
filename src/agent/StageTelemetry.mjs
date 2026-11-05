import { log, stamped } from '../log.mjs';
import { StageTraffic, failureReason, collectRateLimit } from './spawnStage.mjs';
import { formatStreamEvent, StageStats } from './stream.mjs';
import { formatCost } from './cost.mjs';

const SILENCE_REPORT_MS = 30_000;

// Начало ответа модели: с этого момента кэш промпта, записанный запросом,
// доступен соседним запросам.
function isResponseStart(event) {
  return (event?.type === 'stream_event' && event.event?.type === 'message_start')
    || event?.type === 'assistant';
}

export class StageTelemetry {
  #traffic;

  #onResponse;

  constructor({
    stage, artifacts, skills, cwd, onResponse = () => {},
  }) {
    this.#onResponse = onResponse;
    this.stage = stage;
    this.cwd = cwd;
    this.skills = skills;
    this.stream = artifacts.openStream(stage.name);
    this.events = [];
    this.#traffic = new StageTraffic();
    this.lastShown = null;
    this.startedAt = Date.now();
    this.heartbeat = setInterval(() => {
      const silent = Math.round((Date.now() - this.startedAt) / 1000);
      log(`этап «${stage.name}»: ${silent}с без единого события — ${this.traffic.describe()}`);
    }, SILENCE_REPORT_MS);
    this.onEvent = (event) => this.accept(event);
  }

  get traffic() { return this.#traffic; }

  accept(event) {
    if (isResponseStart(event)) this.#onResponse();
    this.stopHeartbeat();
    this.stream.write(event);
    this.events.push(event);
    const shown = formatStreamEvent(event, {
      colors: process.stderr.isTTY, cwd: this.cwd, agent: this.stage.name,
    });
    if (!shown || shown === this.lastShown) return;
    this.lastShown = shown;
    stamped(shown);
  }

  stopHeartbeat() {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = null;
  }

  finish(record) {
    this.stopHeartbeat();
    this.stream.end();

    const stats = StageStats.from(this.events, {
      cwd: this.cwd, skills: this.skills, commands: this.stage.commands,
    });
    for (const line of stats.lines()) log(`[${this.stage.name}] ${line}`);

    const reason = record.ok ? null : failureReason(record);
    const exitDetail = record.ok || record.result
      ? ''
      : ` [exit ${record.exitCode ?? '?'}${record.signal ? `, ${record.signal}` : ''}]`;
    const outcome = record.ok ? 'завершён' : `не выполнен (${reason})${exitDetail}`;
    log(`этап «${this.stage.name}» ${outcome}:`
      + ` ${Math.round(record.durationMs / 1000)}с, ${formatCost(record)}`);
    if (!record.ok) log(`этап «${this.stage.name}»: ${this.traffic.describe()}`);

    return {
      ...record, reason, rateLimit: collectRateLimit(this.events), lostWrites: stats.lostWrites,
    };
  }
}
