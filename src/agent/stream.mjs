import { statSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';
import { HARNESS } from './stageBin.mjs';

// Вызов харнесса в команде Bash, в том числе после `;`, `&&`, `|` и внутри `bash -lc '…'`,
// в которую codex заворачивает команды: имя его команды.
const HARNESS_CALL = new RegExp(String.raw`(?:^|[;&|\n'"])\s*${HARNESS}\s+(\S+)`, 'g');

function statSafe(path) {
  try {
    return statSync(path, { throwIfNoEntry: false });
  } catch (error) {
    // Токены команды — лишь кандидаты в пути; файл, использованный как каталог, совпадением не считается.
    if (error.code === 'ENOTDIR') return undefined;
    throw error;
  }
}

const GREY = '\x1b[90m';
const CYAN = '\x1b[36m';
const YELLOW = '\x1b[33m';
const RESET = '\x1b[0m';

const THINKING = 'размышляет…';

function paint(color, text, colors) {
  return colors ? `${color}${text}${RESET}` : text;
}

function shorten(value, limit = 160) {
  const text = typeof value === 'string' ? value : JSON.stringify(value ?? '');
  const flat = text.replace(/\s+/g, ' ').trim();
  return flat.length > limit ? `${flat.slice(0, limit)}…` : flat;
}

export function relativeToCwd(path, cwd) {
  const text = String(path);
  return cwd && text.startsWith(cwd) ? text.slice(cwd.length).replace(/^\//, '') : text;
}

// Этап читает и `cat`, и `sed -n`, и `grep`, поэтому список прочитанного,
// собранный по одному Read, показывает меньше, чем было.
export function filesIn(command, cwd) {
  const found = [];
  for (const token of String(command).split(/[\s'"|;&()]+/)) {
    if (!token || token.startsWith('-')) continue;
    const path = isAbsolute(token) ? token : join(cwd || '.', token);
    if (statSafe(path)?.isFile()) found.push(relativeToCwd(path, cwd));
  }
  return found;
}

/**
 * content у tool_result бывает и строкой, и массивом блоков. Блоки не всегда
 * текстовые: ToolSearch отвечает ссылками на инструменты (`tool_reference`),
 * и без их разбора отработавший поиск выглядел в логе пустым ответом — то есть
 * неотличимо от сбоя, из-за которого этап якобы ушёл искать грепом.
 */
function toolResultText(block) {
  if (!Array.isArray(block.content)) return block.content;
  return block.content
    .map((part) => part.text ?? (part.type === 'tool_reference' ? part.tool_name : ''))
    .filter(Boolean)
    .join(' ');
}

function blocksOf(event, type) {
  if (event?.type !== type) return [];
  return Array.isArray(event.message?.content) ? event.message.content : [];
}

function formatSystemEvent(event) {
  if (event.subtype === 'status' && event.status === 'requesting') return null;
  if (event.subtype === 'thinking_tokens') return THINKING;
  if (event.subtype === 'init') {
    const mcp = (event.mcp_servers ?? []).map((server) => `${server.name}=${server.status}`);
    return `сессия: ${event.model ?? 'модель не названа'}`
      + `, инструментов ${event.tools?.length ?? 0}`
      + (mcp.length > 0 ? `, MCP ${mcp.join(' ')}` : ', MCP нет');
  }
  if (event.subtype === 'status') return `статус: ${event.status ?? shorten(event)}`;
  const detail = event.hook_name ?? event.message ?? '';
  return `system/${event.subtype ?? 'без подтипа'}${detail ? `: ${shorten(detail, 80)}` : ''}`;
}

const ERROR_MARKS = ['is_error', 'error', 'is_api_error_message', 'api_error_status'];

function errorDump(event) {
  const marked = (source) => ERROR_MARKS.some((mark) => Boolean(source?.[mark]));
  const named = String(event.subtype ?? '').includes('error');
  if (named || marked(event) || marked(event.message)) {
    return `ошибка от драйвера: ${shorten(JSON.stringify(event), 1500)}`;
  }
  return null;
}

function describeCall(input, cwd) {
  if (typeof input !== 'object' || input === null) return String(input);
  const parts = [];
  if (typeof input.command === 'string') parts.push(input.command);
  for (const [key, value] of Object.entries(input)) {
    if (key === 'command') continue;
    const text = typeof value === 'string' ? relativeToCwd(value, cwd) : JSON.stringify(value);
    parts.push(`${key}=${text}`);
  }
  return parts.join(' ');
}

export function formatStreamEvent(event, { colors = true, cwd = '', agent = null } = {}) {
  if (!event || typeof event !== 'object') return null;
  const prefix = agent ? paint(GREY, `[${agent}] `, colors) : '';

  const failure = errorDump(event);
  if (failure) return prefix + paint(YELLOW, failure, colors);

  if (event.type === 'system') {
    const line = formatSystemEvent(event);
    return line === null ? null : prefix + paint(GREY, line, colors);
  }

  if (event.type === 'stage_stderr') {
    return event.text?.trim() ? prefix + paint(YELLOW, `stderr: ${event.text.trim()}`, colors) : null;
  }

  if (event.type === 'stage_raw') {
    return event.text?.trim()
      ? prefix + paint(YELLOW, `вывод не разобран: ${shorten(event.text, 500)}`, colors)
      : null;
  }

  if (event.type === 'assistant') {
    const lines = [];
    for (const block of blocksOf(event, 'assistant')) {
      if (block.type === 'text' && block.text?.trim()) {
        lines.push(prefix + block.text.trim());
      }
      if (block.type === 'thinking' && block.thinking?.trim()) {
        lines.push(prefix + paint(GREY, `размышление: ${shorten(block.thinking, 400)}`, colors));
      }
      if (block.type === 'tool_use') {
        lines.push(prefix + paint(CYAN, `→ ${block.name}: ${describeCall(block.input ?? {}, cwd)}`, colors));
      }
    }
    return lines.length > 0 ? lines.join('\n') : null;
  }

  if (event.type === 'user') {
    const results = blocksOf(event, 'user').filter((block) => block.type === 'tool_result');
    if (results.length === 0) return null;
    return results
      .map((result) => {
        const mark = result.is_error ? paint(YELLOW, '← ошибка', colors) : '←';
        return `${prefix}${mark} ${shorten(toolResultText(result), 200)}`;
      })
      .join('\n');
  }

  if (event.type === 'stream_event' && event.event?.type === 'content_block_start') {
    // Пустой thinking-блок — единственный признак, что модель думает, когда
    // содержимое размышлений не возвращается.
    if (event.event.content_block?.type === 'thinking') {
      return prefix + paint(GREY, THINKING, colors);
    }
  }

  return null;
}

export function parseStreamLine(line) {
  const trimmed = line.trim();
  if (!trimmed) return null;
  try {
    return JSON.parse(trimmed);
  } catch {
    return null;
  }
}

export function isResultEvent(event) {
  return event?.type === 'result';
}

export class StageStats {
  /** `writes` — команды харнесса, которые пишут в журнал: только их сорванный вызов теряет запись. */
  constructor({
    cwd = '', skills = [], commands = [], writes = new Set(),
  } = {}) {
    this.cwd = cwd;
    this.skills = new Set(skills);
    this.commands = commands;
    this.calls = new Map();
    this.files = new Set();
    this.errors = 0;
    this.writes = writes;
    this.harnessWrites = new Set();
    this.lostWrites = 0;
    this.messages = new Set();
    this.looseTurns = 0;
  }

  get turns() {
    return this.messages.size + this.looseTurns;
  }

  static from(events, options) {
    const stats = new StageStats(options);
    for (const event of events) stats.add(event);
    return stats;
  }

  add(event) {
    if (event?.type === 'assistant') {
      const id = event.message?.id;
      if (id) this.messages.add(id);
      else this.looseTurns += 1;
    }
    for (const block of blocksOf(event, 'assistant')) {
      if (block.type !== 'tool_use') continue;
      this.calls.set(block.name, (this.calls.get(block.name) ?? 0) + 1);
      if (block.name === 'Read' && block.input?.file_path) {
        this.files.add(relativeToCwd(block.input.file_path, this.cwd));
      }
      if (block.name === 'Bash' && block.input?.command) {
        const called = [...block.input.command.matchAll(HARNESS_CALL)].map(([, name]) => name);
        if (called.some((name) => this.writes.has(name))) this.harnessWrites.add(block.id);
        for (const file of filesIn(block.input.command, this.cwd)) this.files.add(file);
      }
      if (block.name === 'Skill' && block.input?.skill) {
        this.skills.add(String(block.input.skill));
      }
    }
    for (const block of blocksOf(event, 'user')) {
      if (block.type !== 'tool_result' || !block.is_error) continue;
      this.errors += 1;
      // Отказ харнесса агент чинит повтором; вызов, не дошедший до харнесса
      // (права, песочница, сбой), уносит запись с собой.
      if (this.harnessWrites.has(block.tool_use_id) && !/^отказ:/m.test(String(toolResultText(block)))) {
        this.lostWrites += 1;
      }
    }
  }

  lines() {
    const skills = [...this.skills].sort();
    const files = [...this.files].sort();
    const lines = [];
    lines.push(this.calls.size === 0
      ? 'агент не вызвал ни одного инструмента'
      : `инструменты: ${[...this.calls.entries()]
        .sort((a, b) => b[1] - a[1])
        .map(([name, count]) => `${name}×${count}`)
        .join(', ')}`);
    if (this.errors > 0) lines.push(`отказов: ${this.errors}`);
    if (this.lostWrites > 0) lines.push(`вызовов ${HARNESS}, не дошедших до журнала: ${this.lostWrites}`);
    lines.push(skills.length > 0 ? `скиллы: ${skills.join(', ')}` : 'скиллов у этапа нет');
    lines.push(`команды: ${this.commands.join(', ')}`);
    lines.push(`ходов: ${this.turns}`);
    if (files.length > 0) {
      lines.push(`прочитано файлов: ${files.length}`);
      for (const file of files) lines.push(`  ${file}`);
    }
    return lines;
  }
}
