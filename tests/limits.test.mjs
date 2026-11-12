import { test } from 'node:test';
import assert from 'node:assert/strict';
import { driverLimits, formatLimits, limitsOf } from '../src/agent/limits.mjs';
import { sessionLimits } from '../src/drivers/codex.mjs';

// Дословные события: живой `claude -p --output-format stream-json` 2.1.316 и журналы сессий codex 0.166.
const CLAUDE = JSON.parse('{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1794480600,"rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"org_level_disabled","isUsingOverage":false,"unifiedWindows":{"five_hour":{"utilization":0.45,"resetsAt":1794480600,"status":"allowed"},"seven_day":{"utilization":0.58,"resetsAt":1794787200,"status":"allowed"}}},"uuid":"a7255113-cdfd-4eac-9f94-7a154dcadb53","session_id":"f66bac7e-9b13-4b63-82a4-ae9fce05e825"}');
const CODEX_WEEK = JSON.parse('{"timestamp":"2026-11-06T10:57:40.928Z","ordinal":48,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":156537,"cached_input_tokens":115072,"cache_write_input_tokens":0,"output_tokens":1245,"reasoning_output_tokens":10,"total_tokens":157782},"last_token_usage":{"input_tokens":41598,"cached_input_tokens":40064,"cache_write_input_tokens":0,"output_tokens":449,"reasoning_output_tokens":0,"total_tokens":42047},"last_usage_includes_reasoning":true,"model_context_window":475000},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":70.0,"window_minutes":10080,"resets_at":1794213888},"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":null},"individual_limit":null,"spend_control_reached":null,"plan_type":"self_serve_business_prolite","rate_limit_reached_type":null}}}');
const CODEX_DEPLETED = JSON.parse('{"timestamp":"2026-11-12T09:37:19.666Z","ordinal":13,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"premium","limit_name":null,"primary":null,"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":null},"individual_limit":null,"spend_control_reached":null,"plan_type":null,"rate_limit_reached_type":"workspace_member_credits_depleted"}}}');
const CODEX_DONE = JSON.parse('{"timestamp":"2026-11-12T09:37:19.671Z","ordinal":14,"type":"event_msg","payload":{"type":"task_complete","root_turn_id":"01a1cf1e-5ff6-7bb0-b86d-b74fe3bded24","turn_id":"01a1cf1e-5ff6-7bb0-b86d-b74fe3bded24","last_agent_message":null,"error":{"message":"Your workspace is out of credits. Ask your workspace owner to refill in order to continue.","codex_error_info":"usage_limit_exceeded"},"started_at":1794476236,"completed_at":1794476239,"duration_ms":2880}}');

test('лимиты claude — из rate_limit_event: окна unifiedWindows, доля — в проценты', () => {
  const limits = limitsOf(CLAUDE.rate_limit_info);
  assert.deepEqual(limits, {
    status: 'allowed',
    reason: null,
    windows: { five_hour: { usedPercent: 45, resetsAt: 1794480600 }, seven_day: { usedPercent: 58, resetsAt: 1794787200 } },
  });
  assert.equal(formatLimits('claude', limits), 'лимиты claude: 5 часов — использовано 45%, осталось 55%, сброс 2026-11-12 10:50 UTC;'
    + ' неделя — использовано 58%, осталось 42%, сброс 2026-11-16 00:00 UTC');
  // Форма из документации SDK: одно окно на верхнем уровне, отказ с кодом.
  const rejected = limitsOf({
    status: 'rejected', resetsAt: 1794787200, utilization: 1, errorCode: 'credits_required', rateLimitType: 'seven_day',
  });
  assert.deepEqual(rejected, { status: 'rejected', reason: 'credits_required', windows: { seven_day: { usedPercent: 100, resetsAt: 1794787200 } } });
  assert.equal(formatLimits('claude', rejected),
    'лимиты claude: исчерпан (credits_required); неделя — использовано 100%, осталось 0%, сброс 2026-11-16 00:00 UTC');
  assert.equal(formatLimits('claude', { status: 'allowed_warning', reason: null, windows: {} }), 'лимиты claude: на исходе');
  assert.equal(limitsOf(null), null);
});

test('лимиты codex — из последнего token_count журнала сессии: окно по минутам, исчерпание — отказ без окон', () => {
  assert.deepEqual(sessionLimits([CODEX_WEEK, { type: 'token_usage_record', payload: { usage: {} } }]), {
    status: 'allowed', reason: null, windows: { seven_day: { usedPercent: 70, resetsAt: 1794213888 } },
  });
  const depleted = sessionLimits([CODEX_WEEK, CODEX_DEPLETED, CODEX_DONE]);
  assert.deepEqual(depleted, { status: 'rejected', reason: 'workspace_member_credits_depleted', windows: {} });
  assert.equal(formatLimits('codex', depleted), 'лимиты codex: исчерпан (workspace_member_credits_depleted)');
  assert.equal(sessionLimits([CODEX_DONE]), null, 'нет token_count — нет и сведений');
  // Два окна живьём не встречались: форма — по RateLimitSnapshot из исходников codex, окна — те же записи.
  const { rate_limits: week } = CODEX_WEEK.payload;
  const twoWindows = { payload: { type: 'token_count', rate_limits: { ...week, primary: { used_percent: 12.5, window_minutes: 300, resets_at: 1794480600 }, secondary: week.primary } } };
  assert.deepEqual(sessionLimits([twoWindows]).windows, {
    five_hour: { usedPercent: 12.5, resetsAt: 1794480600 }, seven_day: { usedPercent: 70, resetsAt: 1794213888 },
  });
  const hourly = { payload: { type: 'token_count', rate_limits: { ...week, primary: { ...week.primary, window_minutes: 60 } } } };
  assert.deepEqual(Object.keys(sessionLimits([hourly]).windows), ['60_minutes'], 'окно другой длины — по минутам');
});

test('лимиты прогона — последние сведения каждого драйвера; запись без окон и отказа не в счёт', () => {
  const week = sessionLimits([CODEX_WEEK]);
  const claude = limitsOf(CLAUDE.rate_limit_info);
  assert.deepEqual(driverLimits([
    { driver: 'codex', limits: sessionLimits([CODEX_DEPLETED]) },
    { driver: 'claude', limits: null },
    { driver: 'codex', limits: week },
    { driver: 'claude', limits: claude },
    { driver: 'claude', limits: { status: 'allowed', reason: null, windows: {} } },
    { driver: 'claude', limits: { status: null, reason: null, windows: {} } },
  ]), { codex: week, claude });
});
