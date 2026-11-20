package workflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakastein/cliche"
	"github.com/hakastein/llamer/internal/broker"
	"github.com/hakastein/llamer/internal/config"
)

const noteScript = `const llamer = require("llamer");
exports.definition = {short: "note", long: "Note.", flags: [{name: "text", type: "string", usage: "text"}]};
exports.command = (flags) => { llamer.record({text: flags.text}); return "записано"; };
`

// Отказ пойман скриптом, а вызов всё равно отклонён и этап сорван.
const probeScript = `const llamer = require("llamer");
exports.definition = {short: "probe", long: "Probe.", args: [{name: "kind", type: "string", usage: "kind"}, {name: "target", type: "string", usage: "target"}]};
exports.command = (kind, target) => {
  try {
    const r = kind === "fetch" ? llamer.fetch(target) : llamer.exec(target, [], {stdin: "вход"});
    llamer.record(r);
    return r;
  } catch (e) { return "поймано: " + e.message; }
};
`

type journalLine struct {
	Argv     []string
	Exit     int
	Rejected bool
	Results  []map[string]any
}

// serve — брокер команд workflow с хостами hosts и программами programs на сокете во временном каталоге.
func serve(t *testing.T, hosts, programs []string) (socket string, failed chan error, journalOf func() []journalLine) {
	t.Helper()
	// Не t.TempDir(): имя теста в пути делает путь сокета длиннее лимита unix-сокета macOS (104 байта).
	dir, err := os.MkdirTemp("", "wf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for name, text := range map[string]string{"note.js": noteScript, "probe.js": probeScript} {
		if err := os.MkdirAll(filepath.Join(dir, "commands"), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "commands", name), []byte(text), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	catalog := cliche.Merge(cliche.Config{Sources: []cliche.Source{cliche.Dir(filepath.Join(dir, "commands"))}}.Scan())
	c := &commands{workflow: &config.Workflow{Commands: catalog, Hosts: hosts, Programs: programs}, journal: newJournal(dir)}
	socket, failed = filepath.Join(dir, "s.sock"), make(chan error, 1)
	stop, err := broker.Listen(socket, c.handler("writer", failed))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return socket, failed, func() []journalLine {
		data, err := os.ReadFile(filepath.Join(dir, JournalFile))
		if err != nil {
			t.Fatal(err)
		}
		var out []journalLine
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var l journalLine
			if err := json.Unmarshal([]byte(line), &l); err != nil {
				t.Fatal(err)
			}
			out = append(out, l)
		}
		return out
	}
}

func call(t *testing.T, socket string, argv ...string) broker.Reply {
	t.Helper()
	r, err := broker.Do(context.Background(), socket, broker.Call{Argv: argv, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCommandResultReachesJournal(t *testing.T) {
	socket, failed, journalOf := serve(t, nil, nil)
	text := "строка «один»\nстрока 'два' и \"три\"\n\tконец $HOME `x`"
	r := call(t, socket, "note", "--text", text)
	if r.Exit != 0 || r.Stdout != "записано\n" {
		t.Fatalf("вызов: %+v", r)
	}
	lines := journalOf()
	if len(lines) != 1 || len(lines[0].Results) != 1 || lines[0].Results[0]["text"] != text || lines[0].Rejected {
		t.Fatalf("журнал: %+v", lines)
	}
	if r := call(t, socket, "note", "--txt", "x"); r.Exit != 2 {
		t.Fatalf("неверный вызов: %+v", r)
	}
	select {
	case err := <-failed:
		t.Fatalf("этап сорван: %v", err)
	default:
	}
}

func TestCommandPermissions(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("чужой")) }))
	defer foreign.Close()
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, foreign.URL, http.StatusFound)
			return
		}
		w.Write([]byte("свой"))
	}))
	defer allowed.Close()
	host := strings.TrimPrefix(allowed.URL, "http://")
	// Хост чужого сервера тот же 127.0.0.1: отличает его только порт.
	if u, _ := url.Parse(foreign.URL); u.Hostname() != strings.Split(host, ":")[0] {
		t.Fatal("серверы на разных хостах")
	}

	for _, c := range []struct {
		name   string
		argv   []string
		denied bool
	}{
		{"разрешённый хост", []string{"probe", "fetch", allowed.URL}, false},
		{"разрешённая программа", []string{"probe", "exec", "cat"}, false},
		{"чужой хост", []string{"probe", "fetch", foreign.URL}, true},
		{"переадресация на чужой хост", []string{"probe", "fetch", allowed.URL + "/away"}, true},
		{"учётные данные в адресе", []string{"probe", "fetch", "http://user:pass@" + host}, true},
		{"чужая программа", []string{"probe", "exec", "sh"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			socket, failed, journalOf := serve(t, []string{host}, []string{"cat"})
			r := call(t, socket, c.argv...)
			line := journalOf()[0]
			if !c.denied {
				if r.Exit != 0 || len(line.Results) != 1 || line.Rejected || len(failed) != 0 {
					t.Fatalf("вызов: %+v, журнал: %+v", r, line)
				}
				if got := line.Results[0]; got["body"] != "свой" && got["stdout"] != "вход" {
					t.Fatalf("результат: %v", got)
				}
				return
			}
			if r.Exit == 0 || !line.Rejected || len(line.Results) != 0 || len(failed) != 1 {
				t.Fatalf("вызов: %+v, журнал: %+v", r, line)
			}
		})
	}
}
