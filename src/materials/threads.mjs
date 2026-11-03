import { collectRevisionMarks } from '../gitlab/markdown.mjs';

const THREAD_EXCERPT = 400;

export const THREAD_OURS_OPEN = 'наш, открыт';
export const THREAD_OURS_CLOSED = 'наш, закрыт';
export const THREAD_HUMAN = 'человек';

const THREAD_WITHOUT_TEXT = '(заметка без текста: картинка или предложенная правка)';

function withoutMachineMarks(body) {
  return String(body ?? '')
    .replace(/<sub>ревизия[\s\S]*$/i, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .trim();
}

function threadText(body) {
  const text = withoutMachineMarks(body).replace(/\s+/g, ' ');
  return text.length > THREAD_EXCERPT ? `${text.slice(0, THREAD_EXCERPT)}…` : text;
}

/**
 * Треды реквеста, привязанные к строкам. `full` — анкер и ответы целиком: их
 * печатает команда `thread` этапу тредов, в пакет идут обрезанные `text` и `reply`.
 */
export function summariseThreads(discussions) {
  const threads = [];
  for (const discussion of discussions ?? []) {
    const notes = (discussion.notes ?? []).filter((note) => !note.system);
    const anchor = notes.find((note) => note.position?.position_type === 'text');
    if (!anchor) continue;

    // Наша анкерная нота опознаётся по метке ревизии: имя бота зависит от
    // токена публикации. Ответы по метке опознавать нельзя — кнопка «Quote
    // reply» вставляет цитируемое тело вместе с меткой, и ответ человека
    // выглядел бы нашим.
    const ours = collectRevisionMarks([anchor.body]).length > 0;
    const ourName = ours ? anchor.author?.username : null;
    const resolved = notes.some((note) => note.resolved === true);
    const others = notes.filter((note) => note !== anchor && note.author?.username !== ourName);
    const replies = others.map((note) => threadText(note.body)).filter(Boolean);

    let state = THREAD_HUMAN;
    if (ours) state = resolved ? THREAD_OURS_CLOSED : THREAD_OURS_OPEN;

    threads.push({
      id: discussion.id ?? null,
      ours,
      resolved,
      file: anchor.position.new_path ?? anchor.position.old_path,
      line: anchor.position.line_range?.end?.new_line
        ?? anchor.position.new_line
        ?? anchor.position.old_line,
      state,
      // Тред с пустым телом анкера остаётся в списке: по строке уже есть
      // разговор, и без него разбор напишет замечание второй раз.
      text: threadText(anchor.body) || THREAD_WITHOUT_TEXT,
      reply: replies.length > 0 ? replies.join(' ') : null,
      full: {
        text: withoutMachineMarks(anchor.body),
        replies: others.map((note) => withoutMachineMarks(note.body)).filter(Boolean),
      },
    });
  }
  return threads;
}

export function reviewableThreads(threads) {
  return (threads ?? []).filter((thread) => thread.ours && !thread.resolved && thread.id);
}
