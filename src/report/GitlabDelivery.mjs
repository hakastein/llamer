import { appendFileSync } from 'node:fs';
import { join } from 'node:path';
import { log } from '../log.mjs';
import { findingBody, unanchoredBody, withRevisionMark } from '../gitlab/markdown.mjs';
import { buildThreadPosition, assertAnchored, deletedLineInRequest } from '../gitlab/position.mjs';
import { ASSESSMENT_MARKER } from './risk-note.mjs';
import { formatFailureComment } from './failure.mjs';
import { STATUS_MARKER, nothingOwnNote } from './ReviewReport.mjs';

/**
 * `--publish gitlab`: единственный, кто знает порядок обращений к GitLab —
 * заметка о рисках, закрытие тредов, треды находок, сводка, статус.
 */
export class GitlabDelivery {
  #mr; #runPath; #failed = [];

  constructor({ request, runPath }) {
    this.#mr = request.mr;
    this.#runPath = runPath;
  }

  // `label` — готовый отказ («тред a.php:10 закрыть не удалось»): закрытие треда
  // и публикация находки называют разные действия.
  async attempt(label, call) {
    try {
      await call();
      return true;
    } catch (error) {
      const entry = `${label}: ${error.message}`;
      this.#failed.push(entry);
      log(entry);
      try {
        appendFileSync(join(this.#runPath, 'publish-errors.log'), `${entry}\n`);
      } catch { /* каталог артефактов недоступен — терять из-за этого прогон незачем */ }
      return false;
    }
  }

  #postUnanchored(finding, report) {
    return this.attempt(`замечание ${finding.at} опубликовать не удалось`, async () => {
      await this.#mr.postDiscussion({ body: unanchoredBody(finding, report.markSha) });
      log(`опубликовано замечание ${finding.at}, без привязки к строкам`);
    });
  }

  /**
   * Замечание, которое не встало на строки, всё равно доезжает до автора —
   * заметкой с местом в тексте. Потерять из-за этого разбор дороже, чем
   * показать его без привязки.
   */
  async #postFinding(finding, report) {
    let position;
    try {
      // Новая сторона у изменений задачи и реквеста одна — голова ветки, а
      // старая у каждого своя: удалённые строки переносятся на нумерацию реквеста.
      const inRequest = (line) => (finding.deleted
        ? deletedLineInRequest({
          file: finding.file, line, taskAnchors: report.taskAnchors, requestDiff: report.requestDiff,
        })
        : line);
      position = buildThreadPosition({
        file: finding.file,
        line: inRequest(finding.line),
        lineEnd: inRequest(finding.lineEnd),
        deleted: finding.deleted,
        diff: report.requestDiff,
        version: await this.#mr.version(),
      });
    } catch (error) {
      log(`${finding.at}: ${error.message} — публикуем заметкой`);
      return this.#postUnanchored(finding, report);
    }

    let thread;
    const posted = await this.attempt(`тред ${finding.at} опубликовать не удалось`, async () => {
      thread = await this.#mr.postDiscussion({ body: findingBody(finding, report.markSha), position });
    });
    if (!posted) return false;

    try {
      assertAnchored(thread, finding.file, finding.line);
      log(`опубликован тред ${finding.at}`);
      return true;
    } catch (error) {
      // Нота уже создана и висит в реквесте без места и с неприменимой правкой.
      // Её сносим и повторяем заметкой, иначе автор получает и её, и строку
      // недостачи об одном и том же замечании.
      log(`${finding.at}: ${error.message} — заменяем заметкой`);
      const noteId = thread?.notes?.[0]?.id;
      if (noteId !== undefined) {
        await this.attempt(`непривязанную ноту ${finding.at} убрать не удалось`, () => this.#mr.deleteNote(noteId));
      }
      return this.#postUnanchored(finding, report);
    }
  }

  async deliver(report) {
    if (report.riskNote) {
      await this.attempt('заметку о рисках опубликовать не удалось', async () => {
        await this.#mr.postNote(`${ASSESSMENT_MARKER}\n${report.riskNote}`);
        log('заметка о рисках опубликована');
      });
    }

    for (const thread of report.resolve) {
      const place = thread.place ?? thread.id;
      await this.attempt(`тред ${place} закрыть не удалось`, async () => {
        await this.#mr.resolveDiscussion(thread.id);
        log(`закрыт тред ${place} — ${thread.reason}`);
      });
    }

    const unpublished = [];
    for (const finding of report.findings) {
      if (!await this.#postFinding(finding, report)) unpublished.push(finding);
    }

    const summary = report.summary(unpublished);
    if (summary) {
      await this.attempt('сводку опубликовать не удалось', async () => {
        await this.#mr.postNote(withRevisionMark(summary, report.markSha));
        log('опубликована сводка');
      });
    }
    await this.attempt('статус опубликовать не удалось', () => this.#mr.postNote(report.status(unpublished)));
    return { unpublished, failures: this.#failed };
  }

  async deliverNothingOwn(nothingOwn) {
    // Пустая дельта после прошлого прогона: его статус в MR остаётся в силе, новая заметка — шум.
    if (nothingOwn.task.since) {
      log(`после прошлого прогона ${nothingOwn.task.since.slice(0, 8)} новых своих изменений нет — в MR ничего не публикуется`);
      return { failures: this.#failed };
    }
    await this.attempt('заметку о пропуске ревью опубликовать не удалось', () => this.#mr.postNote(nothingOwnNote(nothingOwn)));
    return { failures: this.#failed };
  }

  async deliverFailure(failure) {
    const body = `${STATUS_MARKER}\n\n${formatFailureComment(failure)}`;
    await this.attempt('сообщение о срыве опубликовать не удалось', () => this.#mr.postNote(body));
    return { failures: this.#failed };
  }
}
