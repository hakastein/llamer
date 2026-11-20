package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// Записи — как их выдают Go encoding/json и Node JSON.stringify, и обходы через \uXXXX.
func TestSecrets(t *testing.T) {
	for i, secret := range []string{`quote-A"value`, `trailing-G\`, `ending-H"`, `html-<b>&c/d`, `кириллица-секрет`, `emoji-😀-key`, `98765432109`, "multi\nline-secret", `{"a":"abcd1234"}`, "node\u2028-\"q-secret"} {
		t.Setenv("REDACT_TEST_"+string(rune('A'+i))+"_TOKEN", secret)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	// Строка JSON с префиксом, а в ней JSON с секретом ещё на три уровня глубже.
	deep := func(v string) string {
		return `{"content":` + q("Result: "+`{"v":`+q(`{"w":`+q(`{"x":`+q(v)+`}`)+`}`)+`}`) + `}`
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
		{"JSONL с оборванной строкой", `{"v":"{\"t\":\"quote-A\\\"value\"}"}` + "\n" + `{"v":"quote-A\"value`, `{"v":"{\"t\":\"***REDACTED***\"}"}` + "\n" + `{"v":"***REDACTED***`},
		{"многострочный секрет в тексте", "error: multi\nline-secret tail", "error: ***REDACTED*** tail"},
		{"секрет-JSON строкой в JSON", `{"content":"{\"a\":\"abcd1234\"}"}`, `{"content":"***REDACTED***"}`},
		{"префикс и \\uXXXX", `{"content":"Result: {\"v\":\"\\u0071uote-A\\\"value\"}"}`, `{"content":"Result: {\"v\":\"***REDACTED***\"}"}`},
		{"префикс, глубина 4 по Go", deep(`quote-A"value`), deep(placeholder)},
		{"JSON.stringify по Node в тексте", "log: \"node\u2028-\\\"q-secret\" end", `log: "***REDACTED***" end`},
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

// Секрет в строке с префиксом на любой глубине вложения JSON не восстанавливается
// раскодированием, выход остаётся JSON; текст без секрета не меняется.
func FuzzSecrets(f *testing.F) {
	for _, s := range []string{`quote-A"value`, `trailing-G\`, `ending-H"`, `html-<b>&c/d`, "emoji-😀-key", `98765432109`, "multi\nline-secret", `{"a":"abcd1234"}`, "node\u2028-\"q-secret"} {
		f.Add(s, uint8(3), "Result: ", `{ "v" : "\u0071uote \"x\" \/", "n": 1.50e+3 }`+"\nlog: \"a\u2028\\\"b\"")
	}
	plain := newSet([]string{"FUZZ_TOKEN=fuzz7secret"})
	f.Fuzz(func(t *testing.T, secret string, depth uint8, prefix, text string) {
		if !recovered(text, "fuzz7secret") && plain.redact(text, false) != text {
			t.Errorf("текст без секрета изменён: %q → %q", text, plain.redact(text, false))
		}
		// Непарная кавычка в префиксе и секрет-пробелы — пределы из shortcut пакета;
		// секрет из знаков разметки JSON и цифр \uXXXX складывается из записи
		// чужих данных: `\u0000` и `000` в тексте дают «0000000».
		if !utf8.ValidString(secret) || strings.ContainsRune(secret, 0) || strings.Trim(secret, " \t\r\n\"\\{}[]:,/u0123456789abcdefABCDEF") == "" ||
			utf8.RuneCountInString(secret) < minSecretLength || strings.Contains(prefix, `"`) {
			return
		}
		in := secret
		for range depth%5 + 1 {
			b, _ := json.Marshal(prefix + in)
			in = `{"v":` + string(b) + `}`
		}
		set := newSet([]string{"FUZZ_TOKEN=" + secret})
		out := set.redact(in, false)
		if !json.Valid([]byte(out)) {
			t.Errorf("выход не JSON: %s", out)
		}
		if recovered(out, secret) {
			t.Errorf("секрет %q восстанавливается из %s", secret, out)
		}
	})
}

// recovered — секрет виден в тексте или в раскодированной строке JSON, начатой
// с любой кавычки, на любой глубине.
func recovered(text, secret string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(text string) bool {
		if seen[text] {
			return false
		}
		seen[text] = true
		if strings.Contains(text, secret) {
			return true
		}
		for i := range len(text) {
			var v string
			if text[i] == '"' && json.NewDecoder(strings.NewReader(text[i:])).Decode(&v) == nil && walk(v) {
				return true
			}
		}
		return false
	}
	return walk(text)
}
