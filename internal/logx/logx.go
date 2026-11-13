// Package logx — лог прогона в stderr: stdout отдан результату, его читает
// запустивший агент.
package logx

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/llamer/internal/redact"
)

// StartedAt — начало прогона.
var StartedAt = time.Now()

var mu sync.Mutex

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

// Log — строка лога llamer.
func Log(message string) { Stamped("[llamer] " + message) }

// Logf — Log с форматом.
func Logf(format string, args ...any) { Log(fmt.Sprintf(format, args...)) }
