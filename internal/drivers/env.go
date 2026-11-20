package drivers

import (
	"os"
	"slices"
	"strings"
)

// Окружение запуска — белый список: токен GitLab, переменные CI и всё прочее,
// чего здесь нет, в процесс модели не попадает. Нужное сверх этого драйверу
// задаёт `drivers.<имя>.env` конфига.
var baseEnv = []string{"PATH", "HOME", "LANG", "TZ", "TMPDIR", "USER", "SHELL", "TERM",
	// сеть и доверие: без них драйвер за прокси не достучится до своего API
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR"}

// Environ — окружение запуска: базовые переменные, переменные входа драйвера auth,
// каталог команд агента первым в PATH и переменные из настроек драйвера.
func (s Setup) Environ(settings Settings, auth []string) []string {
	var out []string
	for _, name := range s.EnvOrder {
		if slices.Contains(baseEnv, name) || strings.HasPrefix(name, "LC_") || slices.Contains(auth, name) {
			if name != "PATH" {
				out = append(out, name+"="+s.Env[name])
			}
		}
	}
	out = append(out, "PATH="+s.StageBin+":"+s.Env["PATH"])
	for _, kv := range settings.Env {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// HomeDir — HOME окружения прогона, без него — HOME процесса llamer.
func HomeDir(env map[string]string) string {
	if h := env["HOME"]; h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}
