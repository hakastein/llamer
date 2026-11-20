// Package stages — этапы прогона и их протокол: оценка, треды, разбирающие
// этапы, дедупликация, скептик.
package stages

import (
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/hakastein/llamer/internal/skills"
)

//go:embed all:skills
var builtinSkills embed.FS

//go:embed templates/*.md
var templates embed.FS

func LoadLibrary(paths []string) (*skills.Library, error) {
	return skills.LoadLibrary(builtinSkills, paths)
}

var placeholder = regexp.MustCompile(`\[([A-Z][A-Z0-9_]*)]`)

// template — задание этапа. Незаполненная дырка уехала бы к модели текстом
// `[PACK]`, поэтому оба перекоса караются на сборке.
type template struct {
	key, text string
	names     []string
}

func loadTemplate(key string) *template {
	data, err := templates.ReadFile("templates/" + key + ".md")
	if err != nil {
		panic(err)
	}
	t := &template{key: key, text: string(data)}
	for _, m := range placeholder.FindAllStringSubmatch(t.text, -1) {
		if !slices.Contains(t.names, m[1]) {
			t.names = append(t.names, m[1])
		}
	}
	return t
}

func (t *template) has(name string) bool { return slices.Contains(t.names, name) }

var blankRuns = regexp.MustCompile(`\n{3,}`)

func (t *template) render(values map[string]string, order []string) (string, error) {
	var missing, extra []string
	for _, n := range t.names {
		if _, ok := values[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("шаблон %s: нечем заполнить %s", t.key, strings.Join(missing, ", "))
	}
	for _, n := range order {
		if !t.has(n) {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		return "", fmt.Errorf("шаблон %s: некуда подставить %s", t.key, strings.Join(extra, ", "))
	}
	text := placeholder.ReplaceAllStringFunc(t.text, func(m string) string { return values[m[1:len(m)-1]] })
	return strings.TrimRightFunc(blankRuns.ReplaceAllString(text, "\n\n"), unicode.IsSpace) + "\n", nil
}
