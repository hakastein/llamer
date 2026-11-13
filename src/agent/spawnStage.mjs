import { spawn } from 'node:child_process';
import { parseStreamLine, isResultEvent } from './stream.mjs';

// Повисший драйвер (модель молчит, сеть висит) иначе держит волну до общего
// таймаута джобы, и прогон падает, не дойдя до публикации собранного. По
// истечении срока этап возвращается невыполненным, и прогон идёт дальше.
const STAGE_TIMEOUT_MS = 20 * 60 * 1000;

const AUTH_STATUSES = new Set([401, 403]);
const RATE_LIMIT_TEXT = /usage limit reached|rate limit|out of credits/i;

// Живые этапы. Каждый — своя группа процессов и убивается группой: убитая одна
// обёртка драйвера (codex на node) оставила бы жить сам драйвер и его команды.
const running = new Set();

function killGroup(child) {
  try {
    process.kill(-child.pid, 'SIGKILL');
  } catch (error) {
    // Группа уже вымерла, а 'close' ещё не пришёл.
    if (error.code !== 'ESRCH') throw error;
  }
}

/** Прогон прерван: все поднятые этапы гибнут вместе со своими процессами. */
export function stopStages() {
  for (const child of running) killGroup(child);
}

export function failureReason(stage) {
  const result = stage?.result ?? null;
  // exitCode 0 не значит успех — при is_error: true причина читается из result,
  // а RATE_LIMIT_TEXT ловит исчерпание подписки и без api_error_status.
  const status = result?.api_error_status;
  if (AUTH_STATUSES.has(status)) return 'auth';
  if (status === 429) return 'rate_limit';
  if (result && RATE_LIMIT_TEXT.test(String(result.result ?? ''))) return 'rate_limit';
  if (stage?.timedOut) return 'timeout';
  if (result === null && stage?.signal) return `killed:${stage.signal}`;
  if (result === null) return 'died';
  return 'other';
}

export function collectRateLimit(events) {
  let last = null;
  for (const event of events) {
    if (event?.type === 'rate_limit_event' && event.rate_limit_info) last = event.rate_limit_info;
  }
  return last;
}

export class StageTraffic {
  constructor() {
    this.stdoutBytes = 0;
    this.events = 0;
    this.firstByteMs = null;
    this.promptSentMs = null;
    this.lastEventType = null;
  }

  record(chunk, startedAt) {
    this.stdoutBytes += chunk.length;
    if (this.firstByteMs === null) this.firstByteMs = Date.now() - startedAt;
  }

  describe() {
    if (this.promptSentMs === null) return 'промпт ещё не дописан в stdin';
    const sent = `промпт отдан за ${Math.round(this.promptSentMs / 1000)}с`;
    if (this.stdoutBytes === 0) return `${sent}, ответа нет: 0 байт на stdout`;
    return `${sent}, ${this.stdoutBytes} байт, событий ${this.events}`
      + `, первый байт на ${Math.round(this.firstByteMs / 1000)}с`
      + `, последнее событие «${this.lastEventType}»`;
  }
}

/**
 * Процесс этапа по спецификации драйвера (`launch`): команда, аргументы,
 * окружение, stdin. Каждая строка потока проходит через `launch.normalize` —
 * дальше все драйверы говорят событиями claude.
 */
export function spawnStage({
  name, launch, cwd, telemetry,
}) {
  const { onEvent, traffic } = telemetry;
  return new Promise((resolve) => {
    const child = spawn(launch.command, launch.argv, {
      cwd,
      // stderr — pipe, а не inherit: на упавшем этапе причина обычно в нём.
      stdio: ['pipe', 'pipe', 'pipe'],
      env: launch.env,
      detached: true,
    });
    child.once('spawn', () => running.add(child));
    // setEncoding до первого 'data': иначе `rest += chunk` режет многобайтовый
    // символ на границе чтения в U+FFFD.
    child.stdout.setEncoding('utf-8');
    child.stderr.setEncoding('utf-8');
    child.stdin.on('error', () => {});

    const startedAt = Date.now();
    // Колбэк end() срабатывает по факту вымывания буфера: промпт больше пайпа,
    // и отметка означает, что процесс его вычитал.
    child.stdin.end(launch.input, () => { traffic.promptSentMs = Date.now() - startedAt; });
    let rest = '';
    let result = null;
    let timedOut = false;

    // Без обработчика 'error' (команда не найдена, нет прав) роняет весь прогон
    // вместо одного невыполненного этапа.
    child.on('error', (error) => {
      onEvent({ type: 'stage_stderr', name, text: `${launch.command} не запустился: ${error.message}` });
    });

    // Страховка на случай гибели процесса без события result: у убитого этапа
    // счётчики иначе теряются, и отчёт показывает ноль при сожжённых миллионах.
    const streamUsage = {
      inputTokens: 0, outputTokens: 0, cacheReadInputTokens: 0, cacheCreationInputTokens: 0,
    };
    let sawUsage = false;
    let contextTokens = 0;
    let turns = 0;
    let sessionId = null;

    const handle = (event) => {
      traffic.events += 1;
      traffic.lastEventType = event.subtype ? `${event.type}/${event.subtype}` : event.type;
      onEvent(event);
      if (isResultEvent(event)) result = event;
      if (event.type === 'system' && event.subtype === 'init') sessionId = event.session_id ?? sessionId;
      if (event.type === 'assistant') turns += 1;
      const usage = event.type === 'assistant' ? event.message?.usage : null;
      if (!usage) return;
      sawUsage = true;
      streamUsage.inputTokens += usage.input_tokens ?? 0;
      streamUsage.outputTokens += usage.output_tokens ?? 0;
      streamUsage.cacheReadInputTokens += usage.cache_read_input_tokens ?? 0;
      streamUsage.cacheCreationInputTokens += usage.cache_creation_input_tokens ?? 0;
      contextTokens = Math.max(contextTokens, (usage.input_tokens ?? 0)
        + (usage.cache_read_input_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0));
    };

    const consume = (line) => {
      const parsed = parseStreamLine(line);
      if (!parsed) {
        if (line.trim()) onEvent({ type: 'stage_raw', name, text: line.trim() });
        return;
      }
      for (const event of launch.normalize(parsed)) handle(event);
    };

    child.stdout.on('data', (chunk) => {
      traffic.record(chunk, startedAt);
      rest += chunk;
      const lines = rest.split('\n');
      rest = lines.pop() ?? '';
      for (const line of lines) consume(line);
    });
    child.stderr.on('data', (chunk) => {
      onEvent({ type: 'stage_stderr', name, text: String(chunk).trimEnd() });
    });

    const timer = setTimeout(() => {
      timedOut = true;
      // SIGKILL, а не SIGTERM: драйвер может игнорировать мягкий сигнал и
      // продолжать держать этап, ради чего таймаут и вводится.
      killGroup(child);
    }, STAGE_TIMEOUT_MS);

    child.on('close', (code, signal) => {
      clearTimeout(timer);
      running.delete(child);
      // result приходит последним; без завершающего перевода строки он осядет
      // в rest, и успешный этап отчитается упавшим.
      if (rest.trim()) consume(rest);
      if (result === null) {
        onEvent({
          type: 'stage_raw',
          name,
          text: `процесс завершился без итогового события: код ${code}, сигнал ${signal ?? 'нет'}`,
        });
      }

      const apiError = result?.is_error === true;
      // Чего нет в потоке драйвера (у codex — usage по запросам), он досчитывает после выхода.
      const late = launch.afterExit?.() ?? {};
      resolve({
        name,
        model: launch.model,
        effort: launch.effort,
        ok: code === 0 && result !== null && !apiError && !timedOut,
        timedOut,
        exitCode: code,
        signal,
        result,
        durationMs: Date.now() - startedAt,
        // null — драйвер цены не назвал (модель codex без цены): в отчёте это `$?`, а не $0.
        cost: result ? (late.cost ?? result.total_cost_usd ?? null) : 0,
        usage: result?.modelUsage ?? (sawUsage ? { stream: streamUsage } : null),
        usageApprox: result?.modelUsage == null && sawUsage,
        contextTokens: Math.max(contextTokens, late.contextTokens ?? 0),
        // usage каждого запроса — у драйвера, что считает их сам (codex); у claude они в сыром потоке.
        requests: late.requests ?? null,
        // лимиты подписки — у драйвера, что берёт их не из потока (codex); у claude их читает телеметрия
        limits: late.limits ?? null,
        turns,
        sessionId,
      });
    });
  });
}
