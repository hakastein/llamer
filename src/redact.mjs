const SECRET_NAME_RE = /(_TOKEN|_SECRET|_PASSWORD|_KEY)$/;

// Короткие значения («true», пароль-заглушка) совпадали бы всюду и превращали
// лог в решето из плейсхолдеров, пряча смысл, а не секрет.
const MIN_SECRET_LENGTH = 7;
const PLACEHOLDER = '***REDACTED***';

/**
 * Значения секретных переменных, от длинных к коротким: если одно — подстрока
 * другого, плейсхолдер иначе разорвал бы внешнее значение на видимые куски.
 * Вместе с каждым — его запись внутри строки JSON: вымарывается и готовый JSON.
 */
function collectSecretValues(env = process.env) {
  const values = [];
  for (const [name, value] of Object.entries(env)) {
    if (typeof value !== 'string' || value.length < MIN_SECRET_LENGTH) continue;
    if (SECRET_NAME_RE.test(name)) values.push(value, JSON.stringify(value).slice(1, -1));
  }
  return [...new Set(values)].sort((a, b) => b.length - a.length);
}

let cachedValues = null;
let cachedEnv = null;

function secretsFor(env) {
  if (env !== cachedEnv || cachedValues === null) {
    cachedValues = collectSecretValues(env);
    cachedEnv = env;
  }
  return cachedValues;
}

/** Замена буквальная (split/join): значение токена содержит спецсимволы регэкспа. */
export function redactSecrets(str, env = process.env) {
  if (typeof str !== 'string' || str.length === 0) return str;
  let out = str;
  for (const value of secretsFor(env)) {
    if (out.includes(value)) out = out.split(value).join(PLACEHOLDER);
  }
  return out;
}
