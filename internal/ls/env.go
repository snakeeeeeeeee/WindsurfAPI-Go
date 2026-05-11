package ls

import "os"

// LSEnvAllowlist 镜像 Node 版 langserver.js 的 LS_ENV_ALLOWLIST。
// 只把 LS 真正需要的 env 透传给子进程；其它上下文（CI token、proxy 密码）不泄漏。
var LSEnvAllowlist = []string{
	"HOME", "PATH", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
}

// BuildChildEnv 构造给 LS 的环境变量数组（`KEY=VAL` 形式，传给 exec.Cmd.Env）。
// 缺省 HOME 时回退到 /tmp，避免 LS 在无 HOME 的 systemd 单元下崩。
// proxyURL 非空时会覆盖 HTTP(S)_PROXY，整进程的 out-going 流量都会走该代理。
func BuildChildEnv(parent []string, proxyURL string) []string {
	source := make(map[string]string, len(parent))
	for _, kv := range parent {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				source[kv[:i]] = kv[i+1:]
				break
			}
		}
	}

	env := make(map[string]string)
	for _, k := range LSEnvAllowlist {
		if v, ok := source[k]; ok && v != "" {
			env[k] = v
		}
	}
	if _, ok := env["HOME"]; !ok {
		if h := os.Getenv("HOME"); h != "" {
			env["HOME"] = h
		} else {
			env["HOME"] = "/tmp"
		}
	}
	if proxyURL != "" {
		env["HTTPS_PROXY"] = proxyURL
		env["HTTP_PROXY"] = proxyURL
		env["https_proxy"] = proxyURL
		env["http_proxy"] = proxyURL
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
