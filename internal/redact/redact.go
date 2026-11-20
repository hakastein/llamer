// Package redact вымарывает значения секретных переменных окружения из текста,
// уходящего наружу.
package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

var secretName = regexp.MustCompile(`(_TOKEN|_SECRET|_PASSWORD|_KEY)$`)

// Короткие значения («true», пароль-заглушка) совпадали бы всюду и превращали
// лог в решето из плейсхолдеров, пряча смысл, а не секрет.
const minSecretLength = 7

const placeholder = "***REDACTED***"

// set — значения секретов и замена в тексте самих значений и их записи внутри
// строки JSON (и с экранированием HTML, как пишет encoding/json).
type set struct {
	values   []string
	replacer *strings.Replacer
}

var secrets = sync.OnceValue(func() *set {
	s := &set{}
	var forms []string
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if utf8.RuneCountInString(value) < minSecretLength || !secretName.MatchString(name) {
			continue
		}
		s.values = append(s.values, value)
		var plain bytes.Buffer
		enc := json.NewEncoder(&plain)
		enc.SetEscapeHTML(false)
		enc.Encode(value) // строка кодируется всегда
		quoted := strings.TrimSpace(plain.String())
		escaped, _ := json.Marshal(value)
		forms = append(forms, value, quoted[1:len(quoted)-1], string(escaped[1:len(escaped)-1]))
	}
	// От длинных к коротким: из совпадений в одной позиции Replacer берёт первое,
	// а более короткое разорвало бы внешнее значение на видимые куски.
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, placeholder)
	}
	s.replacer = strings.NewReplacer(pairs...)
	return s
})

// Secrets — текст без значений секретов окружения процесса. Текст JSON
// (целиком или построчно) остаётся JSON: вымарываются раскодированные строки.
func Secrets(s string) string { return secrets().redact(s) }

func (s *set) redact(text string) string {
	if s.clean(text) {
		return text
	}
	if redacted, ok := s.json(text); ok {
		return redacted
	}
	if strings.Contains(text, "\n") {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			lines[i] = s.redact(line)
		}
		return strings.Join(lines, "\n")
	}
	return s.replacer.Replace(text)
}

// clean — в тексте нет секрета ни в какой записи: всякая запись, кроме
// буквальной, содержит обратную косую черту.
func (s *set) clean(text string) bool {
	if len(s.values) == 0 {
		return true
	}
	if strings.Contains(text, `\`) {
		return false
	}
	for _, v := range s.values {
		if strings.Contains(text, v) {
			return false
		}
	}
	return true
}

// json — текст JSON (одно или несколько значений подряд) с вымаранными
// строками, ключами и числами; ok=false — текст не JSON. Изменённое значение
// пишется заново, остальное — байт-в-байт.
func (s *set) json(text string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var b strings.Builder
	done := 0
	for {
		start := int(dec.InputOffset())
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		var value string
		switch t := token.(type) {
		case string:
			value = t
		case json.Number:
			value = string(t)
		default:
			continue
		}
		redacted := s.value(value)
		if redacted == value {
			continue
		}
		end := int(dec.InputOffset())
		// Перед значением Token съедает пробелы и разделитель.
		start = end - len(strings.TrimLeft(text[start:end], " \t\r\n,:"))
		quoted, _ := json.Marshal(redacted)
		b.WriteString(text[done:start])
		b.Write(quoted)
		done = end
	}
	if done == 0 {
		return text, true
	}
	b.WriteString(text[done:])
	return b.String(), true
}

// value — раскодированная строка: объект или массив JSON в ней разбирается
// тем же путём. Глубина ограничена длиной: каждый уровень вложения хотя бы
// удваивает обратные косые черты.
func (s *set) value(v string) string {
	if s.clean(v) {
		return v
	}
	if t := strings.TrimLeft(v, " \t\r\n"); t != "" && (t[0] == '{' || t[0] == '[') {
		if redacted, ok := s.json(v); ok {
			return redacted
		}
	}
	return s.replacer.Replace(v)
}
