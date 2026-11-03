export function collectShortfall({ state, unpublished = [], notices = [] }) {
  const gaps = [...notices];
  const author = [];
  const ids = (findings) => findings.map((finding) => `#${finding.id}`).join(', ');

  for (const stop of state.stopped) {
    gaps.push(`этап «${stop.name ?? stop.stage}» прекратил работу: ${stop.reason}`);
  }

  for (const failure of state.failures) {
    gaps.push(`этап «${failure.stage}» сорвался: ${failure.reason}`);
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

  return { gaps, author };
}
