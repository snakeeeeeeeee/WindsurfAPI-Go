package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/health"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "配置文件路径；默认优先 configs/default.yaml，缺失时回退 configs/default.example.yaml")
	modelID := flag.String("model", "claude-sonnet-4.6", "用于真实验证的模型")
	timeout := flag.Duration("timeout", 60*time.Second, "单账号验证超时")
	limit := flag.Int("limit", 0, "最多验证多少个账号；0 表示全部")
	apply := flag.Bool("apply", false, "把无效 token 自动标记为 banned/disabled")
	includeDisabled := flag.Bool("include-disabled", false, "包含 disabled/banned 账号")
	smoke := flag.Bool("smoke", true, "执行一次 quota-consuming direct sonnet smoke")
	flag.Parse()

	model := models.GetModelByID(*modelID)
	if model == nil {
		log.Fatalf("unknown model: %s", *modelID)
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
	accounts, err := mgr.GetAllAccounts()
	if err != nil {
		log.Fatal(err)
	}
	client := direct.NewClient(direct.WithTimeout(*timeout))

	total, checked, okCount, invalidCount, failCount := 0, 0, 0, 0, 0
	for i := range accounts {
		a := accounts[i]
		if !*includeDisabled && (!a.Enabled || a.Banned) {
			continue
		}
		if strings.TrimSpace(a.FirebaseToken) == "" {
			continue
		}
		total++
		if *limit > 0 && checked >= *limit {
			continue
		}
		checked++
		result, err := checkOne(client, &a, model, *timeout, *smoke)
		switch result.Status {
		case "ok":
			okCount++
			if *apply {
				note := fmt.Sprintf("account-check ok model=%s plan=%s checked_at=%s", model.ID, result.PlanName, time.Now().Format(time.RFC3339))
				update := health.AccountHealthUpdate(health.TierFromPlan(result.PlanName), result.UserStatus, result.RateLimitedUntil, note)
				update.ModelConfigCount = result.ModelConfigCount
				if updateErr := mgr.UpdateHealthDetails(a.ID, update); updateErr != nil {
					fmt.Printf("account=%d email=%s status=ok update_error=%q\n", a.ID, a.Email, updateErr.Error())
				}
			}
		case "invalid":
			invalidCount++
			if *apply {
				if markErr := mgr.MarkBanned(a.ID); markErr != nil {
					fmt.Printf("account=%d email=%s status=invalid mark_banned_error=%q\n", a.ID, a.Email, markErr.Error())
				}
			}
		default:
			failCount++
		}
		if err != nil {
			fmt.Printf("account=%d email=%s status=%s err=%q\n", a.ID, a.Email, result.Status, truncate(err.Error(), 260))
		}
	}
	fmt.Printf("summary backend=direct model=%s candidates=%d checked=%d ok=%d invalid=%d failed=%d apply=%v smoke=%v\n",
		model.ID, total, checked, okCount, invalidCount, failCount, *apply, *smoke)
}

type checkResult struct {
	Status           string
	PlanName         string
	UserStatus       *direct.UserStatus
	DailyPercent     *float64
	WeeklyPercent    *float64
	ModelConfigCount int
	RateLimitedUntil *time.Time
}

func checkOne(client *direct.Client, a *account.Account, model *models.Model, timeout time.Duration, smoke bool) (checkResult, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	status, err := client.GetUserStatusWithProxy(ctx, a.FirebaseToken, a.ProxyURL)
	if err != nil {
		if isInvalidToken(err) {
			return checkResult{Status: "invalid"}, err
		}
		return checkResult{Status: "failed"}, fmt.Errorf("GetUserStatus: %w", err)
	}
	rl, err := client.CheckMessageRateLimitWithProxy(ctx, a.FirebaseToken, a.ProxyURL)
	if err != nil {
		if isInvalidToken(err) {
			return checkResult{Status: "invalid"}, err
		}
		return checkResult{Status: "failed"}, fmt.Errorf("CheckMessageRateLimit: %w", err)
	}
	cfgs, err := client.GetCascadeModelConfigsWithProxy(ctx, a.FirebaseToken, a.ProxyURL)
	if err != nil {
		if isInvalidToken(err) {
			return checkResult{Status: "invalid"}, err
		}
		return checkResult{Status: "failed"}, fmt.Errorf("GetCascadeModelConfigs: %w", err)
	}
	var retryUntil *time.Time
	if !rl.HasCapacity && rl.RetryAfterMS != nil && *rl.RetryAfterMS > 0 {
		until := time.Now().Add(time.Duration(*rl.RetryAfterMS) * time.Millisecond)
		retryUntil = &until
	}
	text := "skipped"
	if smoke {
		result, err := client.Chat(ctx, direct.ChatRequest{
			APIKey:   a.FirebaseToken,
			ProxyURL: a.ProxyURL,
			Model:    model,
			Messages: []windsurf.ChatMessage{
				{Role: "user", Content: "Reply with exactly: hi"},
			},
		})
		if err != nil {
			if isInvalidToken(err) {
				return checkResult{Status: "invalid"}, err
			}
			return checkResult{Status: "failed"}, fmt.Errorf("Chat: %w", err)
		}
		text = strings.TrimSpace(result.Text)
		if text == "" {
			return checkResult{Status: "failed"}, fmt.Errorf("empty assistant text")
		}
	}
	configCount := len(cfgs.Configs)
	fmt.Printf("account=%d email=%s status=ok plan=%s daily=%s weekly=%s rate_capacity=%v configs=%d elapsed=%s text=%q\n",
		a.ID, a.Email, status.PlanName, pct(status.DailyPercent), pct(status.WeeklyPercent), rl.HasCapacity, configCount, time.Since(start).Round(time.Millisecond), truncate(text, 80))
	return checkResult{
		Status:           "ok",
		PlanName:         status.PlanName,
		UserStatus:       status,
		DailyPercent:     status.DailyPercent,
		WeeklyPercent:    status.WeeklyPercent,
		ModelConfigCount: configCount,
		RateLimitedUntil: retryUntil,
	}, nil
}

func isInvalidToken(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "invalid devin token") ||
		strings.Contains(msg, "failed to validate devin token") ||
		strings.Contains(msg, "logging out and logging in again") ||
		strings.Contains(msg, "authentication failed")
}

func pct(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f%%", *v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
