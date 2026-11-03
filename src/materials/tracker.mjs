const FIELDS = 'idReadable,summary,description,comments(text,author(login))';

export const ISSUE_LIMIT = 12_000;

export const YOUTRACK_ENV = ['YOUTRACK_URL', 'YOUTRACK_TOKEN'];

export function formatIssue(issue) {
  if (!issue || (!issue.summary && !issue.description)) return null;

  const parts = [`### ${issue.idReadable ?? ''} ${issue.summary ?? ''}`.trim()];
  if (issue.description) parts.push(issue.description.trim());

  const comments = (issue.comments ?? [])
    .filter((c) => c.text)
    .map((c) => `- ${c.author?.login ?? 'кто-то'}: ${c.text.trim()}`);
  if (comments.length > 0) parts.push(`#### Комментарии к задаче\n${comments.join('\n')}`);

  const text = parts.join('\n\n');
  return text.length > ISSUE_LIMIT
    ? `${text.slice(0, ISSUE_LIMIT)}\n\n[текст задачи обрезан, остальное — в трекере]`
    : text;
}

/**
 * Трекер задач. Номер задачи ищется регуляркой конфига в имени ветки; текст
 * задачи читает харнесс, а не этапы: токен в дочерний процесс драйвера не уходит.
 */
export class Tracker {
  #config; #env;

  constructor(config, env) {
    this.#config = config;
    this.#env = env;
  }

  get type() { return this.#config.type; }

  issueIdOf(branch) {
    if (this.type === 'none' || !branch) return null;
    return new RegExp(this.#config.issuePattern, 'i').exec(branch)?.[0].toUpperCase() ?? null;
  }

  // Незаданная переменная CI может приехать собственным именем со знаком доллара
  // (`YOUTRACK_URL: $YOUTRACK_URL`), а не пустой строкой.
  missing() {
    if (this.type === 'none') return [];
    return YOUTRACK_ENV.filter((name) => !this.#env[name] || this.#env[name].trim().startsWith('$'));
  }

  async read(issueId) {
    const base = this.#env.YOUTRACK_URL.replace(/\/+$/, '');
    const url = `${base}/api/issues/${encodeURIComponent(issueId)}?fields=${encodeURIComponent(FIELDS)}`;
    const response = await fetch(url, {
      headers: { Authorization: `Bearer ${this.#env.YOUTRACK_TOKEN}`, Accept: 'application/json' },
    });
    if (!response.ok) throw new Error(`YouTrack ответил ${response.status}`);
    return formatIssue(await response.json());
  }
}
