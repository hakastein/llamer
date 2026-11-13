package gitlab

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/git"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	textm "github.com/yuin/goldmark/text"
)

const revisionMark = "llamer-rev"

// StatusMarker — начало заметки-статуса прогона: по ней `--since-last` ищет метку прошлого прогона.
const StatusMarker = "<!-- llamer-status -->"

// EscapeDollarsOutsideCode — GitLab рендерит `$…$` как формулу KaTeX, и текст
// с переменными превращается в кашу. Внутри кода (спанов и блоков) не трогаем:
// там `$` и так не рендерится, а `\$` показался бы буквально.
func EscapeDollarsOutsideCode(text string) string {
	source := []byte(text)
	var code []textm.Segment
	doc := goldmark.DefaultParser().Parse(textm.NewReader(source))
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch {
		case !entering:
		case n.Kind() == ast.KindCodeSpan:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					code = append(code, t.Segment)
				}
			}
			return ast.WalkSkipChildren, nil
		case n.Kind() == ast.KindFencedCodeBlock || n.Kind() == ast.KindCodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				code = append(code, n.Lines().At(i))
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	var b strings.Builder
	last := 0
	for _, s := range code {
		b.WriteString(strings.ReplaceAll(text[last:s.Start], "$", `\$`))
		b.WriteString(text[s.Start:s.Stop])
		last = s.Stop
	}
	b.WriteString(strings.ReplaceAll(text[last:], "$", `\$`))
	return b.String()
}

func FormatRevisionMark(headSha string) string {
	return "<sub>ревизия <code>" + git.Short(headSha) + "</code></sub><!-- " + revisionMark + ":" + headSha + " -->"
}

func WithRevisionMark(body, headSha string) string {
	if headSha == "" {
		return body
	}
	return body + "\n\n" + FormatRevisionMark(headSha)
}

var revisionPattern = regexp.MustCompile(`<!--\s*` + revisionMark + `:([0-9a-f]{7,40})\s*-->`)

// CollectRevisionMarks — ревизии из меток в текстах, без повторов.
func CollectRevisionMarks(bodies []string) []string {
	var found []string
	seen := map[string]bool{}
	for _, body := range bodies {
		for _, m := range revisionPattern.FindAllStringSubmatch(body, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				found = append(found, m[1])
			}
		}
	}
	return found
}

var backticks = regexp.MustCompile("`+")

// withSuggestion — правка, применяемая кнопкой. Смещения в `suggestion:-N+M`
// GitLab считает от строки, на которой стоит комментарий, а у многострочного
// треда это последняя строка диапазона: N = длина диапазона минус один, M = 0.
func withSuggestion(text string, f *domain.Finding, suggestion string) string {
	longest := 0
	for _, run := range backticks.FindAllString(suggestion, -1) {
		longest = max(longest, len(run))
	}
	fence := strings.Repeat("`", max(3, longest+1))
	content := ""
	// Пустая правка — блок без содержимого: кнопка удаляет выделенные строки.
	if suggestion != "" {
		content = suggestion + "\n"
	}
	return text + "\n\n" + fence + "suggestion:-" + strconv.Itoa(f.LineEnd-f.Line) + "+0\n" + content + fence
}

// UnanchoredBody — тело замечания, которое не встало на строки: место
// переезжает в текст. Правка кнопкой из такого комментария не применяется,
// поэтому она идёт обычным блоком кода.
func UnanchoredBody(f *domain.Finding, markSha string) string {
	place := strconv.Itoa(f.Line)
	if f.LineEnd != 0 && f.LineEnd != f.Line {
		place += "-" + strconv.Itoa(f.LineEnd)
	}
	place = "**`" + f.File + ":" + place + "`**"
	if f.Deleted {
		place += " (удалённые строки)"
	}
	parts := []string{place + " — замечание не встало на строки реквеста", EscapeDollarsOutsideCode(f.Text)}
	if f.Suggestion != nil {
		parts = append(parts, "Предлагаемая замена:\n\n```\n"+*f.Suggestion+"\n```")
	}
	return WithRevisionMark(strings.Join(parts, "\n\n"), markSha)
}

// FindingBody — тело инлайн-треда. Экранируется только текст замечания: блок
// `suggestion` GitLab обязан прочитать дословно.
func FindingBody(f *domain.Finding, markSha string) string {
	text := EscapeDollarsOutsideCode(f.Text)
	if f.Suggestion != nil {
		text = withSuggestion(text, f, *f.Suggestion)
	}
	return WithRevisionMark(text, markSha)
}
