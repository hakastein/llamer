const RATE_LIMIT_HEAD = 'Ревью не выполнено: исчерпан лимит подписки драйвера.';

const FAILURE_TEXTS = {
  rate_limit: `${RATE_LIMIT_HEAD} Нужен перезапуск после того, как откроется следующее окно.`,
  auth: 'Ревью не выполнено: авторизация драйвера недействительна — её чинит тот, кто запускает.',
  timeout: 'Ревью не выполнено: этап не уложился в лимит времени и был остановлен.'
    + ' Найденное до остановки осталось в артефактах прогона — перезапусти его.',
  died: 'Ревью не выполнено: процесс этапа завершился, не отдав итог.'
    + ' Найденное до этого осталось в артефактах прогона — перезапусти его.',
  scope: 'Ревью не выполнено: не удалось отделить изменения задачи от чужих.',
  other: 'Ревью не выполнено из-за сбоя прогона.',
};

function formatResetTime(resetsAt) {
  const iso = new Date(resetsAt * 1000).toISOString();
  return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
}

export function formatFailureComment({
  reason, rateLimit = null, detail = null,
}) {
  const text = reason === 'rate_limit' && rateLimit?.resetsAt
    ? `${RATE_LIMIT_HEAD} Окно откроется ${formatResetTime(rateLimit.resetsAt)}`
      + ' — после этого прогон нужно перезапустить.'
    : FAILURE_TEXTS[reason?.startsWith('killed:') ? 'died' : reason] ?? FAILURE_TEXTS.other;
  return `${text}${detail ? `\n\nПричина: ${detail}.` : ''}`;
}
