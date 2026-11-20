// Package redact вымарывает значения секретных переменных окружения из текста,
// уходящего наружу.
package redact

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

var secretName = regexp.MustCompile(`(_TOKEN|_SECRET|_PASSWORD|_KEY)$`)

// Короткие значения («true», пароль-заглушка) совпадали бы всюду и превращали
// лог в решето из плейсхолдеров, пряча смысл, а не секрет.
const minSecretLength = 7

const placeholder = "***REDACTED***"

// Короткие экранирования JSON для управляющих символов.
var shortEscapes = map[rune]string{'\b': "b", '\f': "f", '\n': "n", '\r': "r", '\t': "t"}

// plain — символ, который кодировщики JSON не экранируют: внутри строк JSON
// любой вложенности он записан как есть.
func plain(r rune) bool {
	return r > 0x1f && r < 0x7f && !strings.ContainsRune(`"\/<>&'`, r)
}

// secret — значение секрета в любой записи внутри строк JSON любой вложенности
// и его самый длинный кусок из plain-символов: текст без куска выражение не проверяет.
type secret struct {
	core string
	re   *regexp.Regexp
}

// compile — каждый уровень вложенности удваивает обратные косые черты перед
// экранированием, а не plain-символ бывает записан как есть, коротким
// экранированием или `\uXXXX` (HTML-символы у encoding/json, не-ASCII у
// ensure_ascii, суррогатные пары вне BMP).
func compile(value string) secret {
	var b strings.Builder
	core, run := "", ""
	for _, r := range value {
		if plain(r) {
			b.WriteString(regexp.QuoteMeta(string(r)))
			if run += string(r); len(run) > len(core) {
				core = run
			}
			continue
		}
		run = ""
		alts := []string{regexp.QuoteMeta(string(r))}
		switch r {
		case '\\':
			alts[0] = `\\+`
		case '"', '/':
			alts[0] = `\\*` + alts[0]
		}
		if short, ok := shortEscapes[r]; ok {
			alts = append(alts, `\\+`+short)
		}
		units := []rune{r}
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			units = []rune{hi, lo}
		}
		var escaped strings.Builder
		for _, u := range units {
			fmt.Fprintf(&escaped, `\\+(?i:u%04x)`, u)
		}
		b.WriteString("(?:" + strings.Join(append(alts, escaped.String()), "|") + ")")
	}
	return secret{core: core, re: regexp.MustCompile(b.String())}
}

// Секреты окружения от длинных к коротким: если одно значение — подстрока
// другого, плейсхолдер иначе разорвал бы внешнее на видимые куски.
var secrets = sync.OnceValue(func() []secret {
	var values []string
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if utf8.RuneCountInString(value) >= minSecretLength && secretName.MatchString(name) {
			values = append(values, value)
		}
	}
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	out := make([]secret, len(values))
	for i, v := range values {
		out[i] = compile(v)
	}
	return out
})

// Secrets — текст без значений секретов окружения процесса.
func Secrets(s string) string {
	for _, secret := range secrets() {
		if strings.Contains(s, secret.core) {
			s = secret.re.ReplaceAllLiteralString(s, placeholder)
		}
	}
	return s
}
