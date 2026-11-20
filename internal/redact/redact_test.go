package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Записи — как их выдают Go encoding/json и Node JSON.stringify, и обходы через \uXXXX.
func TestSecrets(t *testing.T) {
	for i, secret := range []string{`quote-A"value`, `trailing-G\`, `ending-H"`, `html-<b>&c/d`, `кириллица-секрет`, `emoji-😀-key`, `98765432109`} {
		t.Setenv("REDACT_TEST_"+string(rune('A'+i))+"_TOKEN", secret)
	}
	for _, c := range []struct{ name, in, want string }{
		{"кавычка", `{"v":"quote-A\"value"}`, `{"v":"***REDACTED***"}`},
		{"ASCII через \\uXXXX", `{"v":"\u0071uote-A\"value"}`, `{"v":"***REDACTED***"}`},
		{"косая в конце перед кавычкой", `{"joke":"trailing-G\\\"tail"}`, `{"joke":"***REDACTED***\"tail"}`},
		{"ложное совпадение с кавычкой строки", `{"v":"ending-H"}`, `{"v":"ending-H"}`},
		{"HTML по Go", `{"v":"html-\u003cb\u003e\u0026c/d"}`, `{"v":"***REDACTED***"}`},
		{"HTML по Node", `{"v":"html-<b>&c\/d"}`, `{"v":"***REDACTED***"}`},
		{"кириллица через \\uXXXX", `{"v":"\u043aириллица-секрет"}`, `{"v":"***REDACTED***"}`},
		{"суррогатная пара", `{"v":"emoji-\ud83d\ude00-key"}`, `{"v":"***REDACTED***"}`},
		{"глубина 0: текст", `ошибка: emoji-😀-key и trailing-G\\ в логе`, `ошибка: ***REDACTED*** и ***REDACTED*** в логе`},
		{"глубина 0: экранированный вид в тексте", `не JSON: "quote-A\"value`, `не JSON: "***REDACTED***`},
		{"глубина 2: JSON в строке", `{"result":"{\"token\":\"quote-A\\\"value\"}"}`, `{"result":"{\"token\":\"***REDACTED***\"}"}`},
		{"глубина 2: JSON внутри текста строки", `{"content":"Result: {\"token\": \"quote-A\\\"value\"}"}`, `{"content":"Result: {\"token\": \"***REDACTED***\"}"}`},
		{"глубина 3 по Node", `{"a":"{\"b\":\"{\\\"c\\\":\\\"html-<b>&c/d\\\"}\"}"}`, `{"a":"{\"b\":\"{\\\"c\\\":\\\"***REDACTED***\\\"}\"}"}`},
		{"неизменённое байт-в-байт",
			`{ "keep" : "\u00e9t\u00e9 \/ x", "n": 1.50e+3, "big": 123456789012345678901234567890, "s": "quote-A\"value", "z": [true, null] }`,
			`{ "keep" : "\u00e9t\u00e9 \/ x", "n": 1.50e+3, "big": 123456789012345678901234567890, "s": "***REDACTED***", "z": [true, null] }`},
		{"число", `{"pin": 98765432109}`, `{"pin": "***REDACTED***"}`},
		{"ключ", `{"quote-A\"value": 1}`, `{"***REDACTED***": 1}`},
		{"JSONL", "{\"a\":\"trailing-G\\\\\\\"x\"}\n{\"b\":1}\n", "{\"a\":\"***REDACTED***\\\"x\"}\n{\"b\":1}\n"},
		{"JSONL с оборванной строкой", "{\"v\":\"ending-H\"}\n{\"v\":\"quote-A\\\"value", "{\"v\":\"ending-H\"}\n{\"v\":\"***REDACTED***"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Secrets(c.in)
			if got != c.want {
				t.Errorf("\nвход   %s\nвыход  %s\nожидаю %s", c.in, got, c.want)
			}
			if strings.HasPrefix(c.in, "{") && json.Valid([]byte(c.in)) && !json.Valid([]byte(got)) {
				t.Errorf("выход не JSON: %s", got)
			}
		})
	}
}
