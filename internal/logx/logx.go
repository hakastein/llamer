// Package logx — лог прогона в stderr с отметкой времени от начала прогона и меткой [llamer], без секретов окружения:
// stdout процесса остаётся его результату. Записи slog из smith идут сюда же через Handler.
package logx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/smith/redact"
)

// StartedAt — начало прогона.
var StartedAt = time.Now()

var mu sync.Mutex

const prefix = "[llamer] "

// SinceStart — миллисекунды от начала прогона.
func SinceStart() int64 { return time.Since(StartedAt).Milliseconds() }

func elapsed() string {
	return fmt.Sprintf("%5d", (SinceStart()+500)/1000)
}

// Stamped пишет строки текста с отметкой времени. Вымарывание здесь, а не у
// вызывающих: в лог идут текст модели, argv и ошибки.
func Stamped(text string) {
	var b strings.Builder
	for _, line := range strings.Split(redact.Secrets(text), "\n") {
		fmt.Fprintf(&b, "[%ss] %s\n", elapsed(), line)
	}
	mu.Lock()
	defer mu.Unlock()
	os.Stderr.WriteString(b.String())
}

// Log — строка лога с меткой [llamer].
func Log(message string) { Stamped(prefix + message) }

// Logf — Log с форматом.
func Logf(format string, args ...any) { Log(fmt.Sprintf(format, args...)) }

// Logger — лог smith: его записи идут сюда же строками Handler.
var Logger = slog.New(Handler{})

const (
	grey   = "\x1b[90m"
	cyan   = "\x1b[36m"
	yellow = "\x1b[33m"
	reset  = "\x1b[0m"
)

func paint(color, text string) string { return color + text + reset }

// Handler — записи slog строками лога. Строка события агента (атрибут event) — «[этап] текст» в цвете её вида,
// без метки [llamer]; иная запись — «[llamer] [этап] сообщение ключ=значение…». Лог раскрашен всегда, и вне терминала.
type Handler struct{ attrs []slog.Attr }

// Enabled — пишется всё.
func (Handler) Enabled(context.Context, slog.Level) bool { return true }

// WithAttrs — обработчик с атрибутами as у каждой записи.
func (h Handler) WithAttrs(as []slog.Attr) slog.Handler {
	return Handler{attrs: append(slices.Clone(h.attrs), as...)}
}

// WithGroup — групп в записях smith нет: имя группы не пишется.
func (h Handler) WithGroup(string) slog.Handler { return h }

// Handle пишет запись r строкой лога.
func (h Handler) Handle(_ context.Context, r slog.Record) error {
	var stage, event string
	failed := false
	var rest []string
	visit := func(a slog.Attr) bool {
		switch {
		case a.Key == "stage":
			stage = a.Value.String()
		case a.Key == "event":
			event = a.Value.String()
		case a.Key == "error" && a.Value.Kind() == slog.KindBool:
			failed = a.Value.Bool()
		default:
			rest = append(rest, " "+a.Key+"="+a.Value.String())
		}
		return true
	}
	for _, a := range h.attrs {
		visit(a)
	}
	r.Attrs(visit)
	if event != "" {
		Stamped(paint(grey, "["+stage+"] ") + eventText(event, r.Message, failed))
		return nil
	}
	if stage != "" {
		stage = "[" + stage + "] "
	}
	Log(stage + r.Message + strings.Join(rest, ""))
	return nil
}

// eventText — строка события агента в цвете её вида: служебное серым, сбой жёлтым, вызов инструмента голубым.
func eventText(kind, text string, failed bool) string {
	switch kind {
	case "session", "note", "thinking":
		return paint(grey, text)
	case "failed", "stderr", "unparsed":
		return paint(yellow, text)
	case "call":
		return paint(cyan, "→ "+text)
	case "reply":
		mark := "←"
		if failed {
			mark = paint(yellow, "← ошибка")
		}
		return mark + " " + text
	}
	return text
}
