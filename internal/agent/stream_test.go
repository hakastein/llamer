package agent_test

import (
	"testing"

	"github.com/hakastein/llamer/internal/agent"
)

func TestLostCommandWriteWithoutRefusalPrefix(t *testing.T) {
	call, err := agent.ParseEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"write","name":"Bash","input":{"command":"save append --text 'done'"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	denied, err := agent.ParseEvent([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"write","is_error":true,"content":"Permission to use Bash has been denied because Claude Code is running in don't ask mode."}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	stats := agent.StatsFrom([]*agent.Event{call, denied}, "", nil, nil, agent.CommandProtocol{Name: "save", Writes: []string{"append"}})
	if stats.LostWrites != 1 {
		t.Fatalf("потеряна ошибка записи команды: %d", stats.LostWrites)
	}
}
