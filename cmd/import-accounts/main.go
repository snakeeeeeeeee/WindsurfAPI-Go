package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)
	tokenRe = regexp.MustCompile(`devin-session-token\$[A-Za-z0-9_.\-]+`)
)

type parsedAccount struct {
	email string
	token string
	proxy string
	line  int
}

type parseWarning struct {
	line int
	msg  string
}

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "配置文件路径；默认优先 configs/default.yaml，缺失时回退 configs/default.example.yaml")
	filePath := flag.String("file", "account.txt", "账号文件路径")
	apply := flag.Bool("apply", false, "真正写入 SQLite；默认只 dry-run")
	flag.Parse()

	items, warnings, err := parseFile(*filePath)
	if err != nil {
		log.Fatal(err)
	}
	seen := map[string]parsedAccount{}
	duplicates := 0
	for _, it := range items {
		if _, ok := seen[it.email]; ok {
			duplicates++
		}
		seen[it.email] = it
	}
	fmt.Printf("parsed=%d unique_emails=%d duplicates=%d invalid_lines=%d apply=%v\n", len(items), len(seen), duplicates, len(warnings), *apply)
	for _, it := range items {
		fmt.Printf("line=%d email=%s token_len=%d proxy=%s\n", it.line, it.email, len(it.token), maskEmpty(it.proxy))
	}
	for _, w := range warnings {
		fmt.Printf("invalid line=%d reason=%s\n", w.line, w.msg)
	}
	if !*apply {
		return
	}
	if len(seen) == 0 {
		log.Fatal("no valid accounts to import")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	sqliteStore, err := store.NewSQLiteStore(cfg.SQLite.Path)
	if err != nil {
		log.Fatal(err)
	}
	defer sqliteStore.Close()
	mgr := account.NewManager(sqliteStore)
	emails := make([]string, 0, len(seen))
	for email := range seen {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	for _, email := range emails {
		it := seen[email]
		if err := mgr.UpsertAccount(it.email, it.token, "imported-from-account-txt", it.proxy, "imported from account.txt"); err != nil {
			log.Fatalf("import %s: %v", it.email, err)
		}
	}
	fmt.Printf("imported=%d db=%s\n", len(seen), cfg.SQLite.Path)
}

func parseFile(path string) ([]parsedAccount, []parseWarning, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var out []parsedAccount
	var warnings []parseWarning
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var email, token string
		var proxy string
		if strings.Contains(raw, "----") {
			parts := strings.Split(raw, "----")
			if len(parts) >= 3 {
				email = emailRe.FindString(strings.TrimSpace(parts[0]))
				token = tokenRe.FindString(strings.TrimSpace(parts[2]))
				if len(parts) >= 4 {
					proxy = proxyValue(strings.TrimSpace(parts[3]))
				}
			}
		}
		if email == "" || token == "" {
			token = tokenRe.FindString(raw)
			tokenPos := strings.Index(raw, token)
			prefix := raw
			if tokenPos >= 0 {
				prefix = raw[:tokenPos]
			}
			if marker := strings.Index(prefix, "----"); marker >= 0 {
				prefix = prefix[:marker]
			}
			email = emailRe.FindString(prefix)
		}
		if email == "" || token == "" {
			warnings = append(warnings, parseWarning{line: line, msg: "expected email and devin-session-token"})
			continue
		}
		out = append(out, parsedAccount{email: email, token: token, proxy: proxy, line: line})
	}
	return out, warnings, sc.Err()
}

func maskEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func proxyValue(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "socks5://") || strings.HasPrefix(lower, "socks5h://") {
		return strings.TrimSpace(s)
	}
	return ""
}
