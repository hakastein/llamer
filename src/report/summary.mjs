export function buildSummaryComment({ summaries = [], gaps = [] } = {}) {
  const parts = summaries
    .filter(({ text }) => String(text ?? '').trim())
    .map(({ stage, text }) => `**${stage}**: ${String(text).trim()}`);
  if (gaps.length > 0) parts.push(['### Ревью неполное', '', ...gaps.map((gap) => `- ${gap}`)].join('\n'));
  return parts.join('\n\n');
}
