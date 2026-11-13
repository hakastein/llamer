package agent

import (
	"bufio"
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
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

// Повисший драйвер (модель молчит, сеть висит) иначе держит волну до общего
// таймаута джобы. По истечении срока этап возвращается невыполненным.
const stageTimeout = 20 * time.Minute

var rateLimitText = regexp.MustCompile(`(?i)usage limit reached|rate limit|out of credits`)

// Живые этапы. Каждый — своя группа процессов и убивается группой: убитая одна
// обёртка драйвера (codex на node) оставила бы жить сам драйвер и его команды.
var (
	runningMu sync.Mutex
	running   = map[int]bool{}
)

func killGroup(pid int) {
	// Группа уже вымерла, а ожидание ещё не вернулось — ESRCH не ошибка.
	syscall.Kill(-pid, syscall.SIGKILL)
}

// StopStages — прогон прерван: все поднятые этапы гибнут вместе со своими
// процессами. Замок остаётся взятым до выхода процесса: этап, дождавшийся
// смерти своего процесса, встаёт на нём и не продолжает прогон, пока прерывание
// убирает за собой, — как у Node, где обработчик сигнала завершает процесс сразу.
func StopStages() {
	runningMu.Lock()
	for pid := range running {
		killGroup(pid)
	}
}

// FailureReason — причина срыва этапа. exitCode 0 не значит успех — при
// is_error причина читается из result, а текст ловит исчерпание подписки и без статуса.
func FailureReason(r *Record) string {
	if r.Result != nil {
		if status := r.Result.APIErrorStatus; status != nil {
			switch *status {
			case 401, 403:
				return "auth"
			case 429:
				return "rate_limit"
			}
		}
		if rateLimitText.MatchString(r.Result.Result) {
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

// CollectRateLimit — последние сведения о лимитах из потока.
func CollectRateLimit(events []*Event) json.RawMessage {
	var last json.RawMessage
	for _, e := range events {
		if e.Type == "rate_limit_event" && len(e.RateLimitInfo) > 0 && string(e.RateLimitInfo) != "null" {
			last = e.RateLimitInfo
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
// разбор потока в события claude.
type Launch struct {
	Command     string
	Argv        []string
	Env         []string
	Input       string
	Model       string
	Effort      string
	Normalize   func(line []byte) [][]byte
	AfterExit   func() Late
	KeepSession func(dir, sessionID string) (string, error)
}

// Late — то, что драйвер досчитывает после выхода (у codex — по журналу сессии).
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

// Spawn — процесс этапа по спецификации драйвера. Каждая строка потока
// проходит через launch.Normalize — дальше все драйверы говорят событиями claude.
func Spawn(name string, launch *Launch, cwd string, t *Telemetry) *Record {
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
	var result *Event
	handle := func(e *Event) {
		traffic.mu.Lock()
		traffic.events++
		traffic.lastEventType = e.Type
		if e.Subtype != "" {
			traffic.lastEventType += "/" + e.Subtype
		}
		traffic.mu.Unlock()
		t.Accept(e)
		switch e.Type {
		case "result":
			result = e
		case "system":
			if e.Subtype == "init" && e.SessionID != "" {
				sessionID = e.SessionID
			}
		case "assistant":
			turns++
			if u := e.Message.Usage; u != nil {
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
		for _, raw := range launch.Normalize([]byte(trimmed)) {
			e, err := ParseEvent(raw)
			if err != nil {
				t.Accept(ownEvent("stage_raw", name, trimmed))
				return
			}
			handle(e)
		}
	}

	path, lookErr := lookPath(launch.Command, launch.Env)
	cmd := &exec.Cmd{Path: path, Args: append([]string{launch.Command}, launch.Argv...), Dir: cwd, Env: launch.Env,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true}}
	var exitCode *int
	signal := ""
	var timedOut atomic.Bool
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
		pid := cmd.Process.Pid
		runningMu.Lock()
		running[pid] = true
		runningMu.Unlock()
		go func() {
			io.WriteString(stdin, launch.Input)
			stdin.Close()
			traffic.mu.Lock()
			traffic.promptSentMs = since()
			traffic.mu.Unlock()
		}()
		timer := time.AfterFunc(stageTimeout, func() {
			timedOut.Store(true)
			// SIGKILL, а не SIGTERM: драйвер может игнорировать мягкий сигнал.
			killGroup(pid)
		})
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
		timer.Stop()
		runningMu.Lock()
		delete(running, pid)
		runningMu.Unlock()
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

	// Чего нет в потоке драйвера (у codex — usage по запросам), он досчитывает после выхода.
	var late Late
	if launch.AfterExit != nil {
		late = launch.AfterExit()
	}
	r := &Record{
		Name: name, Model: launch.Model, Effort: launch.Effort,
		OK:       exitCode != nil && *exitCode == 0 && result != nil && !result.IsError && !timedOut.Load(),
		TimedOut: timedOut.Load(), ExitCode: exitCode, Signal: signal, Result: result,
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
		r.Cost = result.TotalCostUSD
	}
	if result != nil && result.ModelUsage != nil {
		r.Usage = result.ModelUsage
	} else if sawUsage {
		r.Usage = map[string]Usage{"stream": usage}
		r.UsageApprox = true
	}
	return r
}
