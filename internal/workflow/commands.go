package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/logx"
	"github.com/hakastein/llamer/internal/redact"
	"github.com/spf13/cobra"
)

// callTimeout — предел одного вызова команды агентом.
const callTimeout = 10 * time.Minute

// JournalFile — журнал вызовов команд в каталоге прогона: строка JSON на вызов.
const JournalFile = "journal.jsonl"

// commands — команды агента прогона: cliche-скрипты `commands/`, исполняемые на стороне llamer.
type commands struct {
	catalog *cliche.Catalog
	journal *journal
}

// handler — вызовы этапа s с его сокета: агент получает выдачу сразу, а первый срыв уходит в failed, и этап
// срывается после агента.
func (c *commands) handler(s *stage, failed chan<- error) func(context.Context, broker.Call) broker.Reply {
	return func(ctx context.Context, call broker.Call) broker.Reply {
		r, err := c.call(ctx, s, call)
		if err != nil {
			select {
			case failed <- err:
			default:
			}
		}
		return r
	}
}

// call исполняет вызов команды этапом s и пишет его в журнал прогона; выдача вымарана, как запись. Ошибка —
// срыв этапа: отказ в разрешении или незаписанный результат; выдача при этом говорит о нём вызывающему.
func (c *commands) call(ctx context.Context, s *stage, call broker.Call) (broker.Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	a := &api{stage: s, cwd: call.Cwd, journal: c.journal}
	r, results, rejected := c.execute(ctx, s.Allow, call, a)
	r, err := c.journal.call(s.Name, call.Argv, r, rejected, results)
	if err != nil {
		logx.Logf("[%s] вызов агента не записан в журнал: %v", s.Name, err)
		a.fail(fmt.Errorf("вызов %s не записан в журнал: %w", call.Argv[0], err))
		return broker.Reply{Stderr: "llamer не записал вызов в журнал, команда не засчитана; этап завершится сбоем\n", Exit: 1}, a.failure
	}
	return r, a.failure
}

// refusal — отказ в разрешении: хост или программа не названы в спеке этапа.
type refusal struct{ message string }

func (r *refusal) Error() string { return r.message }

// execute исполняет скрипт команды call модулем a; results — записанное им, у неудачного вызова не засчитывается.
// Команда не из allow отклоняется, как неизвестная.
func (c *commands) execute(ctx context.Context, allow []string, call broker.Call, a *api) (broker.Reply, []any, bool) {
	if !slices.ContainsFunc(allow, func(command string) bool {
		words := strings.Fields(command)
		return len(words) <= len(call.Argv) && slices.Equal(words, call.Argv[:len(words)])
	}) {
		return refuse("команда %q этапу не доступна, его команды: %q", call.Argv[0], allow)
	}
	root := &cobra.Command{Use: "llamer"}
	c.catalog.AddTo(root, func(cmd *cobra.Command, command *cliche.Command, in cliche.Input) error {
		value, err := cliche.Run(cmd.Context(), command, in, cliche.Env{Modules: map[string]any{"llamer": a.module()}})
		if err != nil {
			return err
		}
		return print(cmd.OutOrStdout(), value)
	})
	i := slices.IndexFunc(root.Commands(), func(cmd *cobra.Command) bool { return cmd.Name() == call.Argv[0] })
	if i < 0 {
		return refuse("команда %q агенту не доступна", call.Argv[0])
	}
	cmd := root.Commands()[i]
	root.RemoveCommand(cmd) // команда — корень вызова: справка и ошибки называют её саму
	var stdout, stderr strings.Builder
	cmd.SetArgs(call.Argv[1:])
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.CompletionOptions.DisableDefaultCmd = true
	called, err := cmd.ExecuteContextC(ctx)
	r := broker.Reply{Stdout: stdout.String(), Stderr: stderr.String()}
	var usage *cliche.UsageError
	switch {
	case errors.As(err, &usage):
		r.Stderr += usage.Error() + " — справка: " + called.CommandPath() + " --help\n"
		r.Exit = 2
		return r, nil, true
	case a.denied != nil:
		r.Stderr += a.denied.Error() + "; этап завершится сбоем\n"
		r.Exit = 1
		return r, nil, true
	case err != nil:
		r.Stderr += err.Error() + "\n"
		r.Exit = 1
		return r, nil, false
	}
	return r, a.written, false
}

func refuse(format string, args ...any) (broker.Reply, []any, bool) {
	return broker.Reply{Stderr: fmt.Sprintf(format, args...) + "\n", Exit: 2}, nil, true
}

// print — ответ скрипта в stdout строкой с переводом строки в конце: строка как есть, прочее — JSON.
func print(w io.Writer, value any) error {
	if text, ok := value.(string); ok {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		_, err := io.WriteString(w, text)
		return err
	}
	if _, ok := value.(cliche.Undefined); ok || value == nil {
		return nil
	}
	plain, err := plainOf(value)
	if err != nil {
		return err
	}
	data, err := json.Marshal(plain)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// plainOf — значение моста cliche в значении для JSON.
func plainOf(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool, string, int64, float64, *big.Int:
		return v, nil
	case []byte:
		return string(v), nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			p, err := plainOf(item)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	case cliche.Map:
		out := map[string]any{}
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			p, err := plainOf(item)
			if err != nil {
				return nil, err
			}
			out[key] = p
		}
		return out, nil
	}
	return nil, fmt.Errorf("значение %T в JSON не записывается", value)
}

// api — модуль `llamer` скриптов команд: данные этапа, журнал прогона, HTTP к хостам и программы из спека этапа.
type api struct {
	stage   *stage
	cwd     string
	journal *journal
	written []any // записанное этим вызовом
	denied  *refusal
	failure error // первый срыв этапа этим вызовом
}

func (a *api) module() *cliche.Object {
	return cliche.NewObject([]string{"data", "records", "record", "fetch", "exec"}, []any{bridged(a.stage.Data),
		cliche.Func(a.journal.records), cliche.Func(a.record),
		cliche.Func(func(ctx context.Context, args ...any) (any, error) { return fetch(ctx, args, a.allowed) }),
		cliche.Func(func(ctx context.Context, args ...any) (any, error) { return run(ctx, a.cwd, args, a.program) })})
}

// fail — срыв этапа; запоминается первый.
func (a *api) fail(err error) {
	if a.failure == nil {
		a.failure = err
	}
}

// deny — отказ в разрешении: этап сорван, даже если скрипт поймал ошибку.
func (a *api) deny(format string, args ...any) error {
	a.denied = &refusal{fmt.Sprintf(format, args...)}
	a.fail(a.denied)
	return a.denied
}

// record(value) — результат команды в журнал прогона. Незаписываемое значение срывает этап.
func (a *api) record(_ context.Context, args ...any) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("record(value) — ровно одно значение")
	}
	plain, err := plainOf(args[0])
	if err != nil {
		err = fmt.Errorf("результат не записан: %w", err)
		a.fail(err)
		return nil, err
	}
	a.written = append(a.written, plain)
	return cliche.Undefined{}, nil
}

// bridged — значение JSON в значении моста cliche: объект — с ключами по алфавиту, как в JSON журнала.
func bridged(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = bridged(item)
		}
		return out
	case map[string]any:
		keys := slices.Sorted(maps.Keys(v))
		values := make([]any, len(keys))
		for i, key := range keys {
			values[i] = bridged(v[key])
		}
		return cliche.NewObject(keys, values)
	}
	return value
}

// option — поле name объекта настроек вызова строкой; нет поля — пусто.
func option(options cliche.Map, name string) (string, error) {
	if options == nil {
		return "", nil
	}
	value, ok := options.Get(name)
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s — строка", name)
	}
	return text, nil
}

func optionsOf(args []any, at int) (cliche.Map, error) {
	if len(args) <= at {
		return nil, nil
	}
	if _, ok := args[at].(cliche.Undefined); ok || args[at] == nil {
		return nil, nil
	}
	options, ok := args[at].(cliche.Map)
	if !ok {
		return nil, errors.New("настройки вызова — объект")
	}
	return options, nil
}

// allowed — адрес на разрешённом хосте: http или https, без учётных данных, хост или хост:порт назван в fetch этапа.
func (a *api) allowed(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		!(slices.Contains(a.stage.Hosts, u.Host) || slices.Contains(a.stage.Hosts, u.Hostname())) {
		return a.deny("хост %s не разрешён командам этапа (fetch)", u.Redacted())
	}
	return nil
}

// program — программа названа в exec этапа.
func (a *api) program(name string) error {
	if !slices.Contains(a.stage.Programs, name) {
		return a.deny("программа %s не разрешена командам этапа (exec)", name)
	}
	return nil
}

// fetch(url, {method, headers, body}) → {status, body}: HTTP к адресам, которые пропускает allowed, переадресация —
// тоже.
func fetch(ctx context.Context, args []any, allowed func(*url.URL) error) (any, error) {
	address, ok := firstString(args)
	if !ok {
		return nil, errors.New("fetch(url, options) — адрес строкой")
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("адрес не разобран: %w", err)
	}
	if err := allowed(u); err != nil {
		return nil, err
	}
	options, err := optionsOf(args, 1)
	if err != nil {
		return nil, err
	}
	method, err := option(options, "method")
	if err != nil {
		return nil, err
	}
	body, err := option(options, "body")
	if err != nil {
		return nil, err
	}
	// Тело уходит наружу — вымарывается, как вывод; заголовки несут учётные данные запроса и не трогаются.
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), u.String(), strings.NewReader(redact.Secrets(body)))
	if err != nil {
		return nil, err
	}
	if options != nil {
		if headers, ok := options.Get("headers"); ok {
			h, ok := headers.(cliche.Map)
			if !ok {
				return nil, errors.New("headers — объект")
			}
			for _, name := range h.Keys() {
				value, _ := h.Get(name)
				text, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("заголовок %s — строка", name)
				}
				req.Header.Set(name, text)
			}
		}
	}
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("больше 10 переадресаций")
		}
		return allowed(next.URL)
	}}
	resp, err := client.Do(req)
	if denied := (*refusal)(nil); errors.As(err, &denied) {
		return nil, denied
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return cliche.NewObject([]string{"status", "body"}, []any{int64(resp.StatusCode), string(data)}), nil
}

func firstString(args []any) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok
}

// programEnv — окружение программы по белому списку: в окружении llamer лежат токены.
var programEnv = []string{"PATH", "HOME", "LANG"}

// run — exec(program, [args], {stdin}) → {stdout, stderr, exit}: программа, которую пропускает allowed, в каталоге cwd.
func run(ctx context.Context, cwd string, args []any, allowed func(string) error) (any, error) {
	program, ok := firstString(args)
	if !ok {
		return nil, errors.New("exec(program, args, options) — программа строкой")
	}
	if err := allowed(program); err != nil {
		return nil, err
	}
	var argv []string
	if len(args) > 1 {
		if _, undefined := args[1].(cliche.Undefined); !undefined {
			list, ok := args[1].([]any)
			if !ok {
				return nil, errors.New("аргументы программы — массив строк")
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, errors.New("аргументы программы — массив строк")
				}
				argv = append(argv, s)
			}
		}
	}
	options, err := optionsOf(args, 2)
	if err != nil {
		return nil, err
	}
	stdin, err := option(options, "stdin")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, program, argv...)
	agent.Group(cmd)
	cmd.Dir = cwd
	for _, k := range programEnv {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && ctx.Err() == nil) {
		return nil, err
	}
	return cliche.NewObject([]string{"stdout", "stderr", "exit"}, []any{out.String(), errOut.String(), int64(cmd.ProcessState.ExitCode())}), nil
}

// journal — журнал вызовов команд прогона. Вымарывание здесь, в одной точке записи: в строке — ввод агента и
// выдача команд.
type journal struct {
	mu      sync.Mutex
	path    string
	results []recorded
}

// recorded — результат вызова в журнале: этап и значение.
type recorded struct {
	stage string
	value any
}

// records() → журнал прогона: что записали команды всех этапов к этому вызову, [{stage, value}] по порядку записи.
func (j *journal) records(_ context.Context, args ...any) (any, error) {
	if len(args) > 0 {
		return nil, errors.New("records() — без аргументов")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]any, len(j.results))
	for i, r := range j.results {
		out[i] = cliche.NewObject([]string{"stage", "value"}, []any{r.stage, bridged(r.value)})
	}
	return out, nil
}

func newJournal(runPath string) *journal { return &journal{path: filepath.Join(runPath, JournalFile)} }

// call пишет вызов и возвращает выдачу, вымаранную так же, как запись.
func (j *journal) call(stage string, argv []string, r broker.Reply, rejected bool, results []any) (broker.Reply, error) {
	if results == nil {
		results = []any{}
	}
	data, err := json.Marshal(map[string]any{"stage": stage, "argv": argv, "exit": r.Exit, "rejected": rejected,
		"stdout": r.Stdout, "stderr": r.Stderr, "results": results})
	if err != nil {
		return broker.Reply{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return broker.Reply{}, err
	}
	_, err = f.WriteString(redact.Secrets(string(data)) + "\n")
	if err := errors.Join(err, f.Close()); err != nil {
		return broker.Reply{}, err
	}
	for _, value := range results {
		j.results = append(j.results, recorded{stage, value})
	}
	return broker.Reply{Stdout: redact.Secrets(r.Stdout), Stderr: redact.Secrets(r.Stderr), Exit: r.Exit}, nil
}
