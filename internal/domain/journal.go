package domain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const MaxLineBytes = 64 * 1024

// Meta — поля каждого события журнала: тип, кто записал и когда.
type Meta struct {
	Type   string `json:"type"`
	Stage  string `json:"stage,omitempty"`
	Driver string `json:"driver,omitempty"`
	At     int64  `json:"at"`
}

// Header — общие поля события: их заполняет тот, кто пишет журнал.
func (m *Meta) Header() *Meta { return m }

// Event — событие журнала прогона.
type Event interface {
	Kind() string
	Header() *Meta
}

// FindingEvent — новая находка. Suggestion nil — правки нет: пустая правка
// отличается от её отсутствия.
type FindingEvent struct {
	Meta
	File           string    `json:"file"`
	Line           int       `json:"line"`
	LineEnd        int       `json:"lineEnd"`
	Deleted        bool      `json:"deleted"`
	Text           string    `json:"text"`
	Severity       string    `json:"severity"`
	SeverityReason string    `json:"severityReason"`
	Suggestion     *string   `json:"suggestion"`
	Related        []Related `json:"related"`
	Trace          *string   `json:"trace"`
}

// Amend — правка своей находки: только названные поля.
type Amend struct {
	Meta
	ID             int        `json:"id"`
	Text           *string    `json:"text,omitempty"`
	Severity       *string    `json:"severity,omitempty"`
	SeverityReason *string    `json:"severityReason,omitempty"`
	Related        *[]Related `json:"related,omitempty"`
	Suggestion     *string    `json:"suggestion,omitempty"`
}

type Verdict struct {
	Meta
	ID             int     `json:"id"`
	Refuted        bool    `json:"refuted"`
	Reason         string  `json:"reason"`
	Severity       *string `json:"severity"`
	SeverityReason *string `json:"severityReason"`
}

type Dupe struct {
	Meta
	ID     int    `json:"id"`
	Of     int    `json:"of"`
	Reason string `json:"reason"`
}

// Assessment — вердикт оценщика.
type Assessment struct {
	Meta
	Nature string `json:"nature"`
	Risk   string `json:"risk"`
	Reason string `json:"reason"`
	Level  int    `json:"level"`
}

// Resolve — тред, закрытый этапом тредов.
type Resolve struct {
	Meta
	ID     string `json:"id"`
	Place  string `json:"place"`
	Reason string `json:"reason"`
}

// Summary — сводка разбирающего этапа.
type Summary struct {
	Meta
	Text string `json:"text"`
}

// Stop — этап прекратил работу командой stop.
type Stop struct {
	Meta
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

// StageFailed — процесс этапа мог умереть, не успев сказать ни слова. Без
// этой строки «этап не отработал» неотличимо от «этап отработал и промолчал».
// RateLimit — сведения о лимитах из потока драйвера как есть.
type StageFailed struct {
	Meta
	Key       string          `json:"key"`
	Reason    string          `json:"reason"`
	RateLimit json.RawMessage `json:"rateLimit"`
}

func (*FindingEvent) Kind() string { return "finding" }
func (*Amend) Kind() string        { return "amend" }
func (*Verdict) Kind() string      { return "verdict" }
func (*Dupe) Kind() string         { return "dupe" }
func (*Assessment) Kind() string   { return "assess" }
func (*Resolve) Kind() string      { return "resolve" }
func (*Summary) Kind() string      { return "summary" }
func (*Stop) Kind() string         { return "stop" }
func (*StageFailed) Kind() string  { return "stage-failed" }

func newEvent(kind string) Event {
	for _, e := range []Event{&FindingEvent{}, &Amend{}, &Verdict{}, &Dupe{}, &Assessment{}, &Resolve{}, &Summary{}, &Stop{}, &StageFailed{}} {
		if e.Kind() == kind {
			return e
		}
	}
	return nil
}

// decodeEvent — событие строки журнала; nil без ошибки — тип незнаком.
func decodeEvent(line []byte) (Event, error) {
	var head Meta
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, err
	}
	e := newEvent(head.Type)
	if e == nil {
		return nil, nil
	}
	return e, json.Unmarshal(line, e)
}

// EncodeEvent — строка журнала; длиннее предела — отказ: длину текста выбрал
// агент, и чинится это укорачиванием команды, а не остановкой прогона.
func EncodeEvent(e Event) (string, error) {
	e.Header().Type = e.Kind()
	line, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	if len(line) > MaxLineBytes {
		return "", Refuse("строка журнала весит %d байт, предел %d: запись потеряла бы атомарность", len(line), MaxLineBytes)
	}
	return string(line), nil
}

// readEvents — события файла журнала и число битых строк.
func readEvents(file string) ([]Event, int) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, 0
	}
	var events []Event
	broken := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		e, err := decodeEvent(line)
		switch {
		case err != nil:
			broken++
		case e != nil:
			events = append(events, e)
		}
	}
	return events, broken
}

// Journal — журнал одного этапа: один файл по ключу этапа. Каталог прогона
// складывают из таких файлов; свёртка всего прогона — ReadRunState.
type Journal struct {
	Dir  string
	file string
}

func OpenJournal(dir, name string) *Journal {
	return &Journal{Dir: dir, file: filepath.Join(dir, name+".jsonl")}
}

func (j *Journal) Append(e Event) error {
	// Часы шагают и назад (синхронизация времени), а свёртка упорядочивает по `at`:
	// без этого `dupe` и `verdict` встали бы раньше своих находок и пропали.
	e.Header().At = max(time.Now().UnixMilli(), latestAt(j.Dir)+1)
	line, err := EncodeEvent(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// State — состояние на этот момент. Перечитывается с диска каждый раз
// намеренно: этап дописывает журнал, пока харнесс его читает.
func (j *Journal) State() *State {
	events, broken := readEvents(j.file)
	return stateFrom(events, broken)
}

// RecordFailure — срыв этапа в его журнал.
func (j *Journal) RecordFailure(stage, key, reason string, rateLimit json.RawMessage) error {
	return j.Append(&StageFailed{Meta: Meta{Stage: stage}, Key: key, Reason: reason, RateLimit: rateLimit})
}

// State — свёртка событий журнала.
type State struct {
	Findings     []*Finding
	byID         map[int]*Finding
	Assessment   *Assessment
	Summaries    []*Summary // у каждого этапа своя, последняя запись побеждает
	Stopped      []*Stop
	Resolved     []*Resolve
	Failures     []*StageFailed
	SkippedLines int
}

func stateFrom(events []Event, broken int) *State {
	s := &State{byID: map[int]*Finding{}, SkippedLines: broken}
	for _, e := range events {
		s.Apply(e)
	}
	return s
}

// Apply — единственная дверь для записи: знание «как событие меняет
// состояние» принадлежит состоянию, а не командам харнесса.
func (s *State) Apply(event Event) {
	switch e := event.(type) {
	case *FindingEvent:
		f := &Finding{FindingEvent: *e, ID: len(s.Findings) + 1}
		if f.Related == nil {
			f.Related = []Related{}
		}
		s.Findings = append(s.Findings, f)
		s.byID[f.ID] = f
	case *Amend:
		// 'batch' не пишет собственный тип события: в журнал попадают события вложенных команд.
		if f := s.byID[e.ID]; f != nil {
			f.Revise(e)
		}
	case *Verdict:
		if f := s.byID[e.ID]; f != nil {
			f.ApplyVerdict(e)
		}
	case *Dupe:
		f := s.byID[e.ID]
		if f == nil || s.byID[e.Of] == nil || e.ID == e.Of {
			return
		}
		f.DuplicateOf, f.DuplicateReason = e.Of, e.Reason
	case *Assessment:
		s.Assessment = e
	case *Resolve:
		for _, r := range s.Resolved {
			if r.ID == e.ID {
				return
			}
		}
		s.Resolved = append(s.Resolved, e)
	case *Summary:
		for i := range s.Summaries {
			if s.Summaries[i].Stage == e.Stage {
				s.Summaries[i] = e
				return
			}
		}
		s.Summaries = append(s.Summaries, e)
	case *Stop:
		s.Stopped = append(s.Stopped, e)
	case *StageFailed:
		s.Failures = append(s.Failures, e)
	}
}

// Failure — последний срыв.
func (s *State) Failure() *StageFailed {
	if len(s.Failures) == 0 {
		return nil
	}
	return s.Failures[len(s.Failures)-1]
}

func (s *State) Surviving() []*Finding {
	var out []*Finding
	for _, f := range s.Findings {
		if f.Survives() {
			out = append(out, f)
		}
	}
	return out
}

func (s *State) Kept() []*Finding {
	var out []*Finding
	for _, f := range s.Findings {
		if f.DuplicateOf == 0 {
			out = append(out, f)
		}
	}
	return out
}

func (s *State) FindingByID(id int) *Finding { return s.byID[id] }

// HideStages — состояние без находок этих этапов; номера остальных — сквозные по прогону, как были.
func (s *State) HideStages(stages []string) *State {
	var kept []*Finding
	s.byID = map[int]*Finding{}
	for _, f := range s.Findings {
		if slices.Contains(stages, f.Stage) {
			continue
		}
		kept = append(kept, f)
		s.byID[f.ID] = f
	}
	s.Findings = kept
	return s
}

// stageOrder — порядок имён этапов при равном времени записи: пунктуация раньше букв.
func stageOrder(e Event) string { return strings.ReplaceAll(e.Header().Stage, "·", "\x01") }

// foldEvents — состояние из событий, сложенных по времени записи. Порядок по
// `at`, а не по имени файла: номер находки фиксируется на свёртке и
// используется в `verdict --id`, сортировка по имени перенумеровала бы уже выданные id.
func foldEvents(events []Event, broken int) *State {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i].Header().At, events[j].Header().At
		if a != b {
			return a < b
		}
		return stageOrder(events[i]) < stageOrder(events[j])
	})
	return stateFrom(events, broken)
}

// FoldJournals — свёртка журналов этапов.
func FoldJournals(journals []*Journal) *State {
	var events []Event
	broken := 0
	for _, j := range journals {
		e, b := readEvents(j.file)
		events, broken = append(events, e...), broken+b
	}
	return foldEvents(events, broken)
}

func readDirEvents(dir string) ([]Event, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	var events []Event
	broken := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			found, b := readEvents(filepath.Join(dir, e.Name()))
			events, broken = append(events, found...), broken+b
		}
	}
	return events, broken
}

func latestAt(dir string) int64 {
	events, _ := readDirEvents(dir)
	var latest int64
	for _, e := range events {
		latest = max(latest, e.Header().At)
	}
	return latest
}

// ReadRunState — состояние прогона из всех журналов каталога.
func ReadRunState(dir string) *State { return foldEvents(readDirEvents(dir)) }
