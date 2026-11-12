import { formatResetTime } from '../report/failure.mjs';

const WINDOW_TITLES = { five_hour: '5 часов', seven_day: 'неделя' };

const STATUS_TITLES = { allowed_warning: 'на исходе', rejected: 'исчерпан' };

/**
 * Лимиты подписки из `rate_limit_info` события claude: окна по имени,
 * использовано в процентах, сброс — unix-секунды, как у драйвера. Окна — в
 * `unifiedWindows` живого потока; документация знает одно окно на верхнем уровне.
 */
export function limitsOf(info) {
  if (!info) return null;
  const windows = info.unifiedWindows ?? { [info.rateLimitType ?? 'window']: info };
  return {
    status: info.status ?? null,
    reason: info.errorCode ?? null,
    windows: Object.fromEntries(Object.entries(windows).filter(([, window]) => window.utilization != null)
      .map(([name, window]) => [name, { usedPercent: Math.round(window.utilization * 100), resetsAt: window.resetsAt ?? null }])),
  };
}

/**
 * Последние сведения о лимитах каждого драйвера за прогон; драйвер, не
 * приславший ни окна, ни отказа, не попадает.
 * shortcut: «последние» — по порядку этапов, а не по времени события: у
 * параллельных этапов одной волны расходятся на минуты — брать время события, если это станет важно.
 */
export function driverLimits(records) {
  return Object.fromEntries(records
    .filter(({ limits }) => limits && (Object.keys(limits.windows).length > 0 || (limits.status && limits.status !== 'allowed')))
    .map(({ driver, limits }) => [driver, limits]));
}

export function formatLimits(driver, { status, reason, windows }) {
  const parts = Object.entries(windows).map(([name, { usedPercent, resetsAt }]) => `${WINDOW_TITLES[name] ?? name}`
    + ` — использовано ${usedPercent}%, осталось ${100 - usedPercent}%${resetsAt ? `, сброс ${formatResetTime(resetsAt)}` : ''}`);
  if (status && status !== 'allowed') parts.unshift(`${STATUS_TITLES[status] ?? status}${reason ? ` (${reason})` : ''}`);
  return `лимиты ${driver}: ${parts.join('; ')}`;
}
