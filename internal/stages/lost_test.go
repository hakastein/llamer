package stages

import (
	"testing"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/harness"
)

func TestLostWriteWithoutRefusalPrefix(t *testing.T) {
	call := &agent.Call{ID: "write", Name: "Bash", Command: harness.Name + " summary --text 'done'"}
	events := []*agent.Event{
		{Turn: &agent.Turn{Blocks: []agent.Block{{Call: call}}}},
		{Replies: []agent.Reply{{CallID: "write", IsError: true,
			Text: "Permission to use Bash has been denied because Claude Code is running in don't ask mode."}}},
	}
	if lost := lostWrites(events); lost != 1 {
		t.Fatalf("потеряна ошибка записи команды: %d", lost)
	}
}
