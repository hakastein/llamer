// Package broker — тонкий клиент в песочнице агента и его сервер в llamer. Клиент передаёт argv и cwd по
// unix-сокету этапа, llamer исполняет команду и возвращает выдачу. Клиент ставится под именем каждой
// команды workflow: имя, под которым он вызван, — первый элемент argv.
package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// SocketEnv — переменная окружения агента с путём к сокету этапа; её ставит прогон.
const SocketEnv = "LLAMER_HARNESS"

type Call struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

type Reply struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// Main — клиент: name — имя, под которым вызван бинарь, args — аргументы после него.
func Main(name string, args []string, stdout, stderr io.Writer) int {
	socket := os.Getenv(SocketEnv)
	if socket == "" {
		fmt.Fprintf(stderr, "%s: нет %s — команда работает только внутри этапа llamer run\n", name, SocketEnv)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: рабочий каталог не прочитан: %v\n", name, err)
		return 2
	}
	r, err := Do(context.Background(), socket, Call{Argv: append([]string{filepath.Base(name)}, args...), Cwd: cwd})
	if err != nil {
		fmt.Fprintf(stderr, "%s: llamer не ответил: %v\n", name, err)
		return 2
	}
	_, _ = io.WriteString(stdout, r.Stdout)
	_, _ = io.WriteString(stderr, r.Stderr)
	return r.Exit
}

// Do передаёт вызов llamer по сокету этапа.
func Do(ctx context.Context, socket string, c Call) (Reply, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return Reply{}, err
	}
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://harness/call", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Reply{}, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var r Reply
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

// Listen обслуживает вызовы этапа на сокете socket, пока не вызван stop.
func Listen(socket string, handle func(context.Context, Call) Reply) (stop func(), err error) {
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("сокет команд %s: %w", socket, err)
	}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c Call
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil || len(c.Argv) == 0 {
			http.Error(w, "нужен JSON {argv, cwd}", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(handle(r.Context(), c))
	})}
	// Сбой листенера агент увидит отказом вызова, а этап — его отсутствием в журнале.
	go func() { _ = srv.Serve(l) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
