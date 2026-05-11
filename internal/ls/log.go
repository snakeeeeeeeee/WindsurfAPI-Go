package ls

import (
	"bufio"
	"io"
	"log"
	"strings"
)

// streamLSLog 按行读子进程输出并打到主日志。包含 "error" 关键字的走 warn 级别。
func streamLSLog(key string, r io.ReadCloser, stderr bool) {
	if r == nil {
		return
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch {
		case stderr:
			log.Printf("[LS:%s:err] %s", key, line)
		case strings.Contains(strings.ToLower(line), "error"):
			log.Printf("[LS:%s:ERR] %s", key, line)
		default:
			log.Printf("[LS:%s] %s", key, line)
		}
	}
}
