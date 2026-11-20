package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

var rateLimitText = regexp.MustCompile(`(?i)usage limit reached|rate limit|out of credits`)

// FailureReason — причина срыва этапа. exitCode 0 не значит успех — при
// ошибке причина читается из итога, а текст ловит исчерпание подписки и без статуса.
func FailureReason(r *Record) string {
	if r.Result != nil {
		if status := r.Result.APIStatus; status != nil {
			switch *status {
			case 401, 403:
				return "auth"
			case 429:
				return "rate_limit"
			}
		}
		if rateLimitText.MatchString(r.Result.Text) {
			return "rate_limit"
		}
	}
	switch {
	case r.TimedOut:
		return "timeout"
	case r.Result == nil && r.Signal != "":
		return "killed:" + r.Signal
	case r.Result == nil:
		return "died"
	}
	return "other"
}

// lastRateLimit — последние сведения о лимитах из потока; nil — их не было.
func lastRateLimit(events []*Event) *RateLimit {
	var last *RateLimit
	for _, e := range events {
		if e.RateLimit != nil {
			last = e.RateLimit
		}
	}
	return last
}

// Traffic — что известно о вводе-выводе этапа: для сообщений о молчании и срыве.
type Traffic struct {
	mu            sync.Mutex
	stdoutBytes   int
	events        int
	firstByteMs   int64
	promptSentMs  int64
	lastEventType string
}

func newTraffic() *Traffic { return &Traffic{firstByteMs: -1, promptSentMs: -1} }

func (t *Traffic) Describe() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.promptSentMs < 0 {
		return "промпт ещё не дописан в stdin"
	}
	sent := "промпт отдан за " + seconds(t.promptSentMs) + "с"
	if t.stdoutBytes == 0 {
		return sent + ", ответа нет: 0 байт на stdout"
	}
	return fmt.Sprintf("%s, %d байт, событий %d, первый байт на %sс, последнее событие «%s»", sent, t.stdoutBytes, t.events, seconds(t.firstByteMs), t.lastEventType)
}

// Launch — запуск этапа драйвером: команда, аргументы, окружение, stdin и
// разбор потока в общие события.
type Launch struct {
	Command string
	Timeout time.Duration
	Argv    []string
	Env     []string
	Input   string
	Model   string
	Effort  string
	// Parse — события строки потока; ошибка — строка не разобрана.
	Parse       func(line []byte) ([]*Event, error)
	AfterExit   func(sessionID string) Late
	KeepSession func(dir, sessionID string) (string, error)
}

// Late — то, что драйвер досчитывает после выхода, например по журналу сессии.
type Late struct {
	Cost          *float64
	ContextTokens int64
	Requests      []json.RawMessage
	Limits        *Limits
}

// lookPath — команда в PATH окружения этапа, как у spawn Node.
func lookPath(command string, env []string) (string, error) {
	if strings.Contains(command, "/") {
		return command, nil
	}
	path := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, command)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("команды " + command + " нет в PATH этапа")
}

// Spawn — процесс этапа по спецификации драйвера. Каждую строку потока
// launch.Parse переводит в общие события.
func Spawn(ctx context.Context, name string, launch *Launch, cwd string, t *Telemetry) *Record {
	if launch.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, launch.Timeout)
		defer cancel()
	}
	traffic := t.Traffic
	startedAt := time.Now()
	since := func() int64 { return time.Since(startedAt).Milliseconds() }

	// Страховка на случай гибели процесса без события result: у убитого этапа
	// счётчики иначе теряются, и отчёт показывает ноль при сожжённых миллионах.
	var usage Usage
	sawUsage := false
	var contextTokens int64
	turns := 0
	sessionID := ""
	var result *Result
	handle := func(e *Event) {
		traffic.mu.Lock()
		traffic.events++
		traffic.lastEventType = e.Kind
		traffic.mu.Unlock()
		t.Accept(e)
		if e.Result != nil {
			result = e.Result
		}
		if e.Session != nil && e.Session.ID != "" {
			sessionID = e.Session.ID
		}
		if e.Turn != nil {
			turns++
			if u := e.Turn.Usage; u != nil {
				sawUsage = true
				usage.InputTokens += u.InputTokens
				usage.OutputTokens += u.OutputTokens
				usage.CacheReadInputTokens += u.CacheReadInputTokens
				usage.CacheCreationInputTokens += u.CacheCreationInputTokens
				contextTokens = max(contextTokens, u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens)
			}
		}
	}
	consume := func(line string) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return
		}
		events, err := launch.Parse([]byte(trimmed))
		if err != nil {
			t.Accept(ownEvent("stage_raw", name, trimmed))
			return
		}
		for _, e := range events {
			handle(e)
		}
	}

	path, lookErr := lookPath(launch.Command, launch.Env)
	cmd := exec.CommandContext(ctx, path, launch.Argv...)
	// Путь уже найден в PATH запуска, а не в окружении llamer.
	cmd.Path, cmd.Err = path, nil
	cmd.Args[0], cmd.Dir, cmd.Env = launch.Command, cwd, launch.Env
	Group(cmd)
	var exitCode *int
	signal := ""
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	startErr := lookErr
	if startErr == nil {
		startErr = cmd.Start()
	}
	if startErr != nil {
		// Команда не найдена, нет прав — невыполненный этап, а не упавший прогон.
		t.Accept(ownEvent("stage_stderr", name, launch.Command+" не запустился: "+startErr.Error()))
		traffic.mu.Lock()
		traffic.promptSentMs = since()
		traffic.mu.Unlock()
	} else {
		go func() {
			io.WriteString(stdin, launch.Input)
			stdin.Close()
			traffic.mu.Lock()
			traffic.promptSentMs = since()
			traffic.mu.Unlock()
		}()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			buf := make([]byte, 64*1024)
			for {
				n, err := stderr.Read(buf)
				if n > 0 {
					t.Accept(ownEvent("stage_stderr", name, strings.TrimRightFunc(string(buf[:n]), unicode.IsSpace)))
				}
				if err != nil {
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			r := bufio.NewReaderSize(stdout, 64*1024)
			for {
				line, err := r.ReadString('\n')
				if len(line) > 0 {
					traffic.mu.Lock()
					traffic.stdoutBytes += len(line)
					if traffic.firstByteMs < 0 {
						traffic.firstByteMs = since()
					}
					traffic.mu.Unlock()
				}
				// result приходит последним; без завершающего перевода строки он осядет
				// в хвосте, и успешный этап отчитается упавшим.
				consume(line)
				if err != nil {
					return
				}
			}
		}()
		wg.Wait()
		cmd.Wait() // исход — в ProcessState: код или сигнал
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal = unix.SignalName(status.Signal())
		} else {
			c := cmd.ProcessState.ExitCode()
			exitCode = &c
		}
	}
	if result == nil {
		code := "нет"
		if exitCode != nil {
			code = fmt.Sprint(*exitCode)
		}
		sig := signal
		if sig == "" {
			sig = "нет"
		}
		t.Accept(ownEvent("stage_raw", name, "процесс завершился без итогового события: код "+code+", сигнал "+sig))
	}

	// Чего нет в потоке драйвера (usage по запросам), он досчитывает после выхода.
	var late Late
	if launch.AfterExit != nil {
		late = launch.AfterExit(sessionID)
	}
	r := &Record{
		Name: name, Model: launch.Model, Effort: launch.Effort, Events: t.events,
		OK:       exitCode != nil && *exitCode == 0 && result != nil && !result.IsError && ctx.Err() == nil,
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), ExitCode: exitCode, Signal: signal, Result: result,
		DurationMs: since(), ContextTokens: max(contextTokens, late.ContextTokens),
		Requests: late.Requests, Limits: late.Limits, Turns: turns, SessionID: sessionID,
	}
	switch {
	case result == nil:
		zero := 0.0
		r.Cost = &zero
	case late.Cost != nil:
		r.Cost = late.Cost
	default:
		r.Cost = result.Cost
	}
	if result != nil && result.Usage != nil {
		r.Usage = result.Usage
	} else if sawUsage {
		r.Usage = map[string]Usage{"stream": usage}
		r.UsageApprox = true
	}
	return r
}

// Group — команда из CommandContext в своей группе процессов: отмена контекста
// завершает всю группу (обёртка или git могут оставить дочерние команды), а
// ожидание труб после этого ограничено.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Трубы может держать потомок, ушедший из группы.
	cmd.WaitDelay = time.Second
}
