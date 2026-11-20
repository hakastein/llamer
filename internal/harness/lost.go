package harness

import (
	"regexp"
	"slices"

	"github.com/hakastein/llamer/internal/agent"
)

// refusalPrefix — начало строки отказа агенту в stderr команды.
const refusalPrefix = "отказ:"

// writeCommands — команды, которые пишут в журнал: все, кроме печатающих ответ.
var writeCommands = func() []string {
	var out []string
	for _, c := range Commands {
		if !c.Prints {
			out = append(out, c.Name)
		}
	}
	return out
}()

var (
	commandCall = regexp.MustCompile(`(?:^|[;&|\n'"])\s*` + regexp.QuoteMeta(Name) + `\s+(\S+)`)
	refusalLine = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(refusalPrefix))
)

// LostWrites — вызовы записи харнесса в потоке этапа, не дошедшие до журнала. Отказ
// харнесса агент чинит повтором; отказ в запуске команды теряет запись.
func LostWrites(events []*agent.Event) int {
	writes := map[string]bool{}
	lost := 0
	for _, e := range events {
		if e.Turn != nil {
			for _, b := range e.Turn.Blocks {
				if b.Call == nil {
					continue
				}
				for _, m := range commandCall.FindAllStringSubmatch(b.Call.Command, -1) {
					if slices.Contains(writeCommands, m[1]) {
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
