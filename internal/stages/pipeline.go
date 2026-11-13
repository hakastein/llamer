package stages

import (
	"sync"

	"github.com/hakastein/llamer/internal/agent"
	"github.com/hakastein/llamer/internal/domain"
	"github.com/hakastein/llamer/internal/logx"
)

// Outcome — итог прогона этапов: единственное, что пересекает границу pipeline → cli.
type Outcome struct {
	Journal    *domain.State
	Assessment *domain.Assessment
	// Failure — последний срыв волны оценки, для сообщения о неудаче.
	Failure *domain.StageFailed
	// Records — записи отработавших этапов: стоимость, токены, длительность.
	Records []*agent.Record
}

func (o *Outcome) OK() bool { return o.Assessment != nil }

// Run — протокол прогона: волна оценки (оценщик и треды) → проверка вердикта →
// волна разбирающих этапов на глубине от оценщика, экземпляр на каждый драйвер
// этапа → дедупликация → скептик, каждому — свёртка предыдущих журналов. В
// каждой волне первым идёт ведущий этап, остальные — с началом его ответа.
func Run(tk *Toolkit) (*Outcome, error) {
	tk.protocol = protocol{tk.Materials}
	assess := NewAssess(tk)
	opening := []*Stage{assess, NewThreads(tk)}
	if err := runWave(assess, opening, 0, nil); err != nil {
		return nil, err
	}

	// Без вердикта оценщика глубина разбора неизвестна — дальше не идём.
	assessment := assess.State().Assessment
	if assessment == nil {
		return &Outcome{Journal: fold(opening), Failure: assess.State().Failure(), Records: records(opening)}, nil
	}
	logx.Logf("оценка: %s, риск %s, уровень %d", assessment.Nature, assessment.Risk, assessment.Level)

	level := assessment.Level
	var review []*Stage
	for _, settings := range tk.Config.AnalysisStages() {
		for _, d := range settings.Drivers {
			review = append(review, NewAnalysis(tk, settings, d))
		}
	}
	leader := review[0]
	for _, s := range review {
		if s.applies(tk.Materials, nil) {
			leader = s
			break
		}
	}
	if err := runWave(leader, review, level, nil); err != nil {
		return nil, err
	}

	prior := append(append([]*Stage{}, opening...), review...)
	dedupe := NewDedupe(tk)
	if err := dedupe.Run(level, fold(prior)); err != nil {
		return nil, err
	}
	skeptic := NewSkeptic(tk)
	if err := skeptic.Run(level, fold(append(prior, dedupe))); err != nil {
		return nil, err
	}
	all := append(prior, dedupe, skeptic)
	return &Outcome{Journal: fold(all), Assessment: assessment, Records: records(all)}, nil
}

// runWave — волна этапов, ведущий первым. Общую часть пакета ведущий пишет в
// кэш, а запись доступна соседним запросам с началом ответа модели: стартовав
// вместе с ним, этапы волны записали бы её каждый сам. Ждать приходится секунды.
func runWave(leader *Stage, wave []*Stage, assessed int, journal *domain.State) error {
	errs := make([]error, len(wave))
	var wg sync.WaitGroup
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = wave[i].Run(assessed, journal)
		}()
	}
	for i, s := range wave {
		if s == leader {
			start(i)
		}
	}
	<-leader.Responding()
	for i, s := range wave {
		if s != leader {
			start(i)
		}
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func fold(stages []*Stage) *domain.State {
	journals := make([]*domain.Journal, len(stages))
	for i, s := range stages {
		journals[i] = s.Journal()
	}
	return domain.FoldJournals(journals)
}

// records — не поднимавшийся этап записи не оставляет.
func records(stages []*Stage) []*agent.Record {
	out := []*agent.Record{}
	for _, s := range stages {
		if r := s.Record(); r != nil {
			out = append(out, r)
		}
	}
	return out
}
