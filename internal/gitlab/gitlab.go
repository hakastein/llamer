// Package gitlab — merge request в GitLab и разметка заметок LLaMer.
package gitlab

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/hakastein/llamer/internal/redact"
)

const PerPage = 100

var mrURL = regexp.MustCompile(`^(https?://[^/]+)/(.+?)/-/merge_requests/(\d+)`)
var mrShort = regexp.MustCompile(`^([\w./-]+)!(\d+)$`)

// URLEnv — переменные с адресом GitLab для ссылки вида group/project!123.
var URLEnv = []string{"GITLAB_URL", "CI_SERVER_URL"}

// Ref — разобранная ссылка на merge request; URL пуст, если адрес неизвестен.
type Ref struct{ URL, ProjectPath, IID string }

// ParseRef — `https://host/group/project/-/merge_requests/123` несёт адрес
// GitLab сам, `group/project!123` берёт его из окружения.
func ParseRef(ref string, env map[string]string) (Ref, error) {
	if m := mrURL.FindStringSubmatch(ref); m != nil {
		return Ref{m[1], m[2], m[3]}, nil
	}
	if m := mrShort.FindStringSubmatch(ref); m != nil {
		base := ""
		for _, name := range URLEnv {
			if env[name] != "" {
				base = strings.TrimRight(env[name], "/")
				break
			}
		}
		return Ref{base, m[1], m[2]}, nil
	}
	return Ref{}, fmt.Errorf("не разобрать ссылку на merge request: %s\nожидается https://gitlab.example.com/group/project/-/merge_requests/123 или group/project!123", ref)
}

// MergeRequest — один MR. Тексты заметок уходят без значений секретов.
type MergeRequest struct {
	client      *gl.Client
	project     string
	IID         int64
	version     *gl.MergeRequestDiffVersion
	discussions []*gl.Discussion
	loaded      bool
}

func NewMergeRequest(ref Ref, token string) (*MergeRequest, error) {
	client, err := gl.NewClient(token, gl.WithBaseURL(ref.URL))
	if err != nil {
		return nil, err
	}
	iid, err := strconv.ParseInt(ref.IID, 10, 64)
	if err != nil {
		return nil, err
	}
	return &MergeRequest{client: client, project: ref.ProjectPath, IID: iid}, nil
}

func (m *MergeRequest) Info() (*gl.MergeRequest, error) {
	info, _, err := m.client.MergeRequests.GetMergeRequest(m.project, m.IID, nil)
	return info, err
}

// MergeDiff — дифф самого GitLab: `git diff base...HEAD` на ветке с влитой
// целевой тянет чужие правки. Без `access_raw_diffs` часть файлов приезжает
// схлопнутой — с пустым `diff`, по которому позицию треда не собрать; в
// клиенте эндпоинт `changes` устарел и файлов не отдаёт — запрос собирается здесь.
func (m *MergeRequest) MergeDiff() ([]*gl.MergeRequestDiff, error) {
	path := fmt.Sprintf("projects/%s/merge_requests/%d/changes", gl.PathEscape(m.project), m.IID)
	req, err := m.client.NewRequest(http.MethodGet, path, &struct {
		AccessRawDiffs bool `url:"access_raw_diffs"`
	}{true}, nil)
	if err != nil {
		return nil, err
	}
	var changes struct {
		Changes []*gl.MergeRequestDiff `json:"changes"`
	}
	_, err = m.client.Do(req, &changes)
	return changes.Changes, err
}

// Version — последняя версия диффа MR: позиции тредов строятся от неё.
func (m *MergeRequest) Version() (*gl.MergeRequestDiffVersion, error) {
	if m.version != nil {
		return m.version, nil
	}
	versions, _, err := m.client.MergeRequests.GetMergeRequestDiffVersions(m.project, m.IID, nil)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("у MR !%d нет версий — публиковать инлайн-треды не к чему", m.IID)
	}
	m.version = versions[0]
	return m.version, nil
}

// Discussions — все треды MR. GitLab отдаёт старые записи первыми: по одной
// странице на длинном MR не видно свежих тредов.
func (m *MergeRequest) Discussions() ([]*gl.Discussion, error) {
	for page := int64(1); !m.loaded; page++ {
		opt := &gl.ListMergeRequestDiscussionsOptions{ListOptions: gl.ListOptions{PerPage: PerPage, Page: page}}
		chunk, _, err := m.client.Discussions.ListMergeRequestDiscussions(m.project, m.IID, opt)
		if err != nil {
			return nil, err
		}
		m.discussions = append(m.discussions, chunk...)
		m.loaded = len(chunk) < PerPage
	}
	return m.discussions, nil
}

func (m *MergeRequest) PostDiscussion(body string, position *gl.PositionOptions) (*gl.Discussion, error) {
	opt := &gl.CreateMergeRequestDiscussionOptions{Body: gl.Ptr(redact.Secrets(body)), Position: position}
	created, _, err := m.client.Discussions.CreateMergeRequestDiscussion(m.project, m.IID, opt)
	return created, err
}

func (m *MergeRequest) DeleteNote(noteID int64) error {
	_, err := m.client.Notes.DeleteMergeRequestNote(m.project, m.IID, noteID)
	return err
}

func (m *MergeRequest) ResolveDiscussion(id string) error {
	_, _, err := m.client.Discussions.ResolveMergeRequestDiscussion(m.project, m.IID, id,
		&gl.ResolveMergeRequestDiscussionOptions{Resolved: gl.Ptr(true)})
	return err
}

func (m *MergeRequest) PostNote(body string) error {
	opt := &gl.CreateMergeRequestNoteOptions{Body: gl.Ptr(redact.Secrets(EscapeDollarsOutsideCode(body)))}
	_, _, err := m.client.Notes.CreateMergeRequestNote(m.project, m.IID, opt)
	return err
}
