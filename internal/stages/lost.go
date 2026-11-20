package stages

import (
	"regexp"
	"slices"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/harness"
)

var (
	harnessCall = regexp.MustCompile(`(?:^|[;&|\n'"])\s*` + regexp.QuoteMeta(harness.Name) + `\s+(\S+)`)
	refusalLine = regexp.MustCompile(`(?m)^отказ:`)
)

// lostWrites — вызовы записи харнесса в потоке этапа, не дошедшие до журнала. Отказ
// харнесса агент чинит повтором; отказ в запуске команды теряет запись.
func lostWrites(events []*agent.Event) int {
	writes := map[string]bool{}
	lost := 0
	for _, e := range events {
		if e.Turn != nil {
			for _, b := range e.Turn.Blocks {
				if b.Call == nil {
					continue
				}
				for _, m := range harnessCall.FindAllStringSubmatch(b.Call.Command, -1) {
					if slices.Contains(harness.WriteCommands, m[1]) {
						writes[b.Call.ID] = true
					}
				}
			}
		}
		for _, r := range e.Replies {
			if r.IsError && writes[r.CallID] && !refusalLine.MatchString(r.Text) {
				lost++
			}
		}
	}
	return lost
}
