// Package redact вымарывает значения секретных переменных окружения из текста,
// уходящего наружу.
//
// shortcut: остаются секрет на границе токенов JSON верхнего уровня (`secret","tail`), секрет из одних пробелов между токенами JSON и строка JSON в тексте после непарной кавычки, переделать, если такой вывод даст агент или сериализатор.
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

var secrets = sync.OnceValue(func() *set { return newSet(os.Environ()) })

// newSet — секреты из записей окружения вида ИМЯ=значение.
func newSet(environ []string) *set {
	s := &set{}
	var forms []string
	for _, entry := range environ {
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
}

// Secrets — текст без значений секретов окружения процесса. Текст JSON
// (одно значение или несколько подряд, как JSONL) остаётся JSON: вымарываются
// раскодированные строки.
func Secrets(s string) string { return secrets().redact(s, false) }

// redact — текст без секретов. Буквальная замена идёт после разбора: до него
// она рвала бы запись строки внутри (`\u0000` и `0…` секрета) и прятала её
// содержимое от разбора. В сыром JSON её нет вовсе: она ломала бы JSON и
// ловила кавычку строки как конец секрета. decoded — раскодированная строка
// JSON: она пишется обратно строкой, поэтому замена в ней безопасна даже для
// JSON внутри и ловит секрет, который сам — JSON.
// Глубина ограничена длиной: раскодированная строка короче своей записи.
func (s *set) redact(text string, decoded bool) string {
	if s.clean(text) {
		return text
	}
	if redacted, ok := s.json(text); ok {
		if decoded {
			return s.replacer.Replace(redacted)
		}
		return redacted
	}
	return s.replacer.Replace(s.literals(text))
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
		var value, redacted string
		switch t := token.(type) {
		case string:
			value, redacted = t, s.redact(t, true)
		case json.Number:
			value = string(t)
			redacted = s.replacer.Replace(value) // запись числа и есть значение: разбор бы зациклился
		default:
			continue
		}
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

// literals — текст не JSON, где вымарана каждая строка JSON внутри (JSON после
// префикса, JSON.stringify в логе). Кавычки спариваются слева направо; перевод
// строки и другой управляющий символ рвут пару: в строке JSON их не бывает.
func (s *set) literals(text string) string {
	var b strings.Builder
	done := 0
	for i := strings.IndexByte(text, '"'); i >= 0; {
		j := i + 1
		for j < len(text) && text[j] != '"' && text[j] >= 0x20 {
			if text[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(text) {
			break
		}
		if text[j] == '"' {
			var v string
			if json.Unmarshal([]byte(text[i:j+1]), &v) == nil {
				if redacted := s.redact(v, true); redacted != v {
					quoted, _ := json.Marshal(redacted)
					b.WriteString(text[done:i])
					b.Write(quoted)
					done = j + 1
				}
			}
			j++
		}
		next := strings.IndexByte(text[j:], '"')
		if next < 0 {
			break
		}
		i = j + next
	}
	if done == 0 {
		return text
	}
	b.WriteString(text[done:])
	return b.String()
}
