import { escapeDollarsOutsideCode } from './markdown.mjs';
import { redactSecrets } from '../redact.mjs';

export const PER_PAGE = 100;

const MR_URL = /^(https?:\/\/[^/]+)\/(.+?)\/-\/merge_requests\/(\d+)/;

export const GITLAB_URL_ENV = ['GITLAB_URL', 'CI_SERVER_URL'];

export class MrRefError extends Error {}

/**
 * `https://host/group/project/-/merge_requests/123` несёт адрес GitLab сам,
 * `group/project!123` берёт его из окружения.
 */
export function parseMrRef(ref, env = {}) {
  const url = MR_URL.exec(ref ?? '');
  if (url) return { url: url[1], projectPath: url[2], iid: url[3] };

  const short = /^([\w./-]+)!(\d+)$/.exec(ref ?? '');
  if (short) {
    const base = GITLAB_URL_ENV.map((name) => env[name]).find(Boolean) ?? null;
    return { url: base?.replace(/\/+$/, '') ?? null, projectPath: short[1], iid: short[2] };
  }

  throw new MrRefError(`не разобрать ссылку на merge request: ${ref}\n`
    + 'ожидается https://gitlab.example.com/group/project/-/merge_requests/123 или group/project!123');
}

/** REST API GitLab через fetch: токен — заголовком, тело — JSON без значений секретов. */
export class GitlabApi {
  #url; #token;

  constructor({ url, token }) {
    this.#url = url.replace(/\/+$/, '');
    this.#token = token;
  }

  async call(method, path, body = null) {
    const response = await fetch(`${this.#url}/api/v4/${path}`, {
      method,
      headers: {
        'PRIVATE-TOKEN': this.#token,
        ...(body === null ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === null ? undefined : redactSecrets(JSON.stringify(body)),
    });
    const text = await response.text();
    if (!response.ok) {
      throw new Error(`GitLab API отказал: ${method} ${path} → ${response.status} ${text.slice(0, 300)}`);
    }
    return text ? JSON.parse(text) : null;
  }

  /**
   * Полный список постраничного эндпоинта. GitLab режет ответ на страницы и отдаёт
   * старые записи первыми: по одной странице на длинном MR не видно свежих тредов.
   */
  async pages(path) {
    const all = [];
    for (let page = 1; ; page += 1) {
      const chunk = await this.call('GET', `${path}${path.includes('?') ? '&' : '?'}per_page=${PER_PAGE}&page=${page}`);
      all.push(...chunk);
      if (chunk.length < PER_PAGE) return all;
    }
  }
}

export class MergeRequest {
  #api; #cachedVersion = null; #cachedDiscussions = null;

  constructor({ api, projectPath, iid }) {
    this.#api = api;
    this.projectPath = projectPath;
    this.iid = iid;
  }

  get path() {
    return `projects/${encodeURIComponent(this.projectPath)}/merge_requests/${this.iid}`;
  }

  async info() {
    const mr = await this.#api.call('GET', this.path);
    return {
      title: mr.title,
      sourceBranch: mr.source_branch,
      targetBranch: mr.target_branch,
      baseSha: mr.diff_refs?.base_sha ?? null,
      headSha: mr.diff_refs?.head_sha ?? null,
    };
  }

  // Дифф самого GitLab: `git diff base...HEAD` на ветке с влитой целевой тянет
  // чужие правки. Без `access_raw_diffs` часть файлов приезжает схлопнутой —
  // с пустым `diff`, по которому позицию треда не собрать.
  async mergeDiff() {
    const { changes } = await this.#api.call('GET', `${this.path}/changes?access_raw_diffs=true`);
    return (changes ?? []).map((entry) => ({
      path: entry.new_path,
      oldPath: entry.old_path,
      born: Boolean(entry.new_file),
      deleted: Boolean(entry.deleted_file),
      renamed: Boolean(entry.renamed_file),
      diff: entry.diff ?? '',
    }));
  }

  async version() {
    if (this.#cachedVersion) return this.#cachedVersion;
    const versions = await this.#api.call('GET', `${this.path}/versions`);
    if (versions.length === 0) {
      throw new Error(`у MR !${this.iid} нет версий — публиковать инлайн-треды не к чему`);
    }
    const [latest] = versions;
    this.#cachedVersion = {
      baseSha: latest.base_commit_sha,
      headSha: latest.head_commit_sha,
      startSha: latest.start_commit_sha,
    };
    return this.#cachedVersion;
  }

  async discussions() {
    this.#cachedDiscussions ??= await this.#api.pages(`${this.path}/discussions`);
    return this.#cachedDiscussions;
  }

  postDiscussion({ body, position }) {
    return this.#api.call('POST', `${this.path}/discussions`, { body, ...(position ? { position } : {}) });
  }

  deleteNote(noteId) {
    return this.#api.call('DELETE', `${this.path}/notes/${encodeURIComponent(noteId)}`);
  }

  resolveDiscussion(id) {
    return this.#api.call('PUT', `${this.path}/discussions/${encodeURIComponent(id)}`, { resolved: true });
  }

  postNote(body) {
    return this.#api.call('POST', `${this.path}/notes`, { body: escapeDollarsOutsideCode(body) });
  }
}
