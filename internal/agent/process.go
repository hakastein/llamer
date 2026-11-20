package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/hakastein/llamer/internal/logx"
	"golang.org/x/sys/unix"
)

// Command — процесс этапа: программа, аргументы, окружение, рабочий каталог и задание в stdin.
type Command struct {
	Path    string // имя программы в PATH окружения Env или путь к ней
	Argv    []string
	Env     []string
	Dir     string
	Input   string
	Timeout time.Duration // 0 — без предела
}

// Exit — исход процесса этапа.
type Exit struct {
	Code   *int // nil — процесс не запустился или убит сигналом
	Signal string
	// TimedOut — вышел срок этапа; Interrupted — этап не доработал: вышел срок или прогон отменён.
	TimedOut, Interrupted bool
	DurationMs            int64
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

// Process — запущенный процесс этапа: его stdout читается через Lines до конца, затем Wait.
type Process struct {
	ctx       context.Context
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	t         *Telemetry
	stdout    io.Reader // nil — процесс не запустился
	stderr    sync.WaitGroup
	startedAt time.Time
}

func (p *Process) since() int64 { return time.Since(p.startedAt).Milliseconds() }

// Start — процесс этапа в своей группе: задание уходит в stdin, stderr — в
// телеметрию t. Незапустившийся процесс — невыполненный этап, а не упавший
// прогон: причина уходит в телеметрию, Lines пуст.
func Start(ctx context.Context, c Command, t *Telemetry) *Process {
	p := &Process{t: t, startedAt: time.Now()}
	if c.Timeout > 0 {
		p.ctx, p.cancel = context.WithTimeout(ctx, c.Timeout)
	} else {
		p.ctx, p.cancel = context.WithCancel(ctx)
	}
	logx.Logf("этап «%s» запускается: %s %s", t.name, c.Path, strings.Join(c.Argv, " "))
	path, err := lookPath(c.Path, c.Env)
	p.cmd = exec.CommandContext(p.ctx, path, c.Argv...)
	// Путь уже найден в PATH запуска, а не в окружении llamer.
	p.cmd.Path, p.cmd.Err = path, nil
	p.cmd.Args[0], p.cmd.Dir, p.cmd.Env = c.Path, c.Dir, c.Env
	Group(p.cmd)
	stdin, _ := p.cmd.StdinPipe()
	stdout, _ := p.cmd.StdoutPipe()
	stderr, _ := p.cmd.StderrPipe()
	if err == nil {
		err = p.cmd.Start()
	}
	traffic := t.traffic
	if err != nil {
		t.own("stage_stderr", c.Path+" не запустился: "+err.Error())
		traffic.mu.Lock()
		traffic.promptSentMs = p.since()
		traffic.mu.Unlock()
		return p
	}
	p.stdout = stdout
	go func() {
		io.WriteString(stdin, c.Input)
		stdin.Close()
		traffic.mu.Lock()
		traffic.promptSentMs = p.since()
		traffic.mu.Unlock()
	}()
	p.stderr.Add(1)
	go func() {
		defer p.stderr.Done()
		buf := make([]byte, 64*1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				t.own("stage_stderr", strings.TrimRightFunc(string(buf[:n]), unicode.IsSpace))
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

// Lines — непустые строки stdout без краевых пробелов; все строки как есть уходят в сырой
// поток этапа. Читается до конца: иначе Wait ждёт процесс, которому некуда писать.
func (p *Process) Lines() iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		if p.stdout == nil {
			return
		}
		traffic := p.t.traffic
		r := bufio.NewReaderSize(p.stdout, 64*1024)
		for {
			// Итог приходит последним; без завершающего перевода строки он осядет
			// в хвосте, и успешный этап отчитается упавшим.
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				p.t.stream.Raw([]byte(strings.TrimSuffix(line, "\n")))
				traffic.mu.Lock()
				traffic.stdoutBytes += len(line)
				if traffic.firstByteMs < 0 {
					traffic.firstByteMs = p.since()
				}
				traffic.mu.Unlock()
			}
			if trimmed := strings.TrimSpace(line); trimmed != "" && !yield([]byte(trimmed)) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

// Wait — исход процесса после того, как Lines прочитан.
func (p *Process) Wait() Exit {
	defer p.cancel()
	var exit Exit
	if p.stdout != nil {
		p.stderr.Wait()
		p.cmd.Wait() // исход — в ProcessState: код или сигнал
		if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			exit.Signal = unix.SignalName(status.Signal())
		} else {
			code := p.cmd.ProcessState.ExitCode()
			exit.Code = &code
		}
	}
	exit.TimedOut = errors.Is(p.ctx.Err(), context.DeadlineExceeded)
	exit.Interrupted = p.ctx.Err() != nil
	exit.DurationMs = p.since()
	return exit
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
