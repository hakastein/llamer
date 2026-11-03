import { redactSecrets } from './redact.mjs';

export const STARTED_AT = Date.now();

export function elapsed() {
  return String(Math.round((Date.now() - STARTED_AT) / 1000)).padStart(5, ' ');
}

// Весь лог — в stderr: stdout отдан JSON-отчёту, его читает запустивший агент.
// Вымарывание здесь, а не у вызывающих: в лог идут текст модели, argv и ошибки.
export function stamped(text) {
  for (const line of redactSecrets(String(text)).split('\n')) process.stderr.write(`[${elapsed()}s] ${line}\n`);
}

export function log(message) {
  stamped(`[llamer] ${message}`);
}
