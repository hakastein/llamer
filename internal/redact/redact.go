// Package redact вымарывает значения секретных переменных окружения из текста,
// уходящего наружу.
package redact

import (
	"bytes"
	"encoding/json"
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

// Значения секретных переменных, от длинных к коротким: если одно — подстрока
// другого, плейсхолдер иначе разорвал бы внешнее значение на видимые куски.
// Вместе с каждым — его запись внутри строки JSON (и с экранированием HTML,
// как пишет encoding/json): вымарывается и готовый JSON.
var secrets = sync.OnceValue(func() []string {
	var values []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if utf8.RuneCountInString(value) < minSecretLength || !secretName.MatchString(name) {
			continue
		}
		add(value)
		var plain bytes.Buffer
		enc := json.NewEncoder(&plain)
		enc.SetEscapeHTML(false)
		enc.Encode(value) // строка кодируется всегда
		quoted := strings.TrimSpace(plain.String())
		add(quoted[1 : len(quoted)-1])
		escaped, _ := json.Marshal(value)
		add(string(escaped[1 : len(escaped)-1]))
	}
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return values
})

// Secrets — текст без значений секретов окружения процесса; замена буквальная.
func Secrets(s string) string {
	if s == "" {
		return s
	}
	for _, value := range secrets() {
		s = strings.ReplaceAll(s, value, placeholder)
	}
	return s
}
