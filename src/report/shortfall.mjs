/**
 * Сорванный экземпляр этапа, чей этап отработал на другом драйвере, — не
 * недостача: разбор этапа есть. О нём сообщается, прогон не проваливается.
 */
export function collectShortfall({ state, unpublished = [], records = [] }) {
  const gaps = [];
  const author = [];
  const notices = [];
  const done = new Set(records.filter((record) => record.ok).map((record) => record.key));
  const ids = (findings) => findings.map((finding) => `#${finding.id}`).join(', ');

  for (const stop of state.stopped) {
    gaps.push(`этап «${stop.name ?? stop.stage}» прекратил работу: ${stop.reason}`);
  }

  let failed = 0;
  for (const failure of state.failures) {
    const line = `этап «${failure.stage}» сорвался: ${failure.reason}`;
    if (done.has(failure.key)) {
      const notice = `${line} — этап «${failure.key}» отработал на другом драйвере`;
      notices.push(notice);
      author.push(notice);
    } else {
      failed += 1;
      gaps.push(line);
    }
  }

  const noVerdict = state.findings.filter((finding) => finding.awaitsVerdict);
  if (noVerdict.length > 0) {
    gaps.push(`скептик не проверил находок: ${noVerdict.length} (${ids(noVerdict)})`);
  }

  if (state.assessment === null) gaps.push('вердикта оценщика в журнале нет');
  if (state.skippedLines > 0) {
    gaps.push(`битых строк журнала: ${state.skippedLines} — столько записей потеряно`);
  }

  if (unpublished.length > 0) {
    const line = `не опубликовано замечаний: ${unpublished.length} (${unpublished.map((finding) => finding.at).join(', ')})`;
    gaps.push(line);
    author.push(line);
  }

  return {
    gaps, author, notices, failed,
  };
}
