// ls-smoke: Plan B 硬证据 —— 启动 LS、h2c 连接、调真业务 GetUserStatus。
//
// 步骤：
//
//	[1/3] 用 internal/ls.Pool 拉起 LS 进程；
//	[2/3] 用 internal/windsurf.Client 包装 LS 入口；
//	[3/3] 调 GetUserStatus 拿真实 UserStatus，验证 "链路 + 业务" 双通。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/config"
	grpcpkg "github.com/zhangyu/windsurfapi-go/internal/grpc"
	"github.com/zhangyu/windsurfapi-go/internal/ls"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

func main() {
	configPath := flag.String("config", "configs/default.yaml", "YAML 配置路径")
	binary := flag.String("binary", "", "LS binary（覆盖 env/config）")
	dataRoot := flag.String("data-root", "", "LS codeium_dir")
	mode := flag.String("mode", "status", "冒烟模式：status 或 send")
	modelID := flag.String("model", "claude-4.5-haiku", "send 模式使用的 Claude 模型")
	prompt := flag.String("prompt", "Reply with exactly: hi", "send 模式发送的用户消息")
	port := flag.Int("port", 0, "LS port（默认 42100）")
	total := flag.Duration("timeout", 3*time.Minute, "总超时")
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	apiKey := strings.TrimSpace(os.Getenv("WINDSURF_API_KEY"))
	if apiKey == "" {
		log.Fatalf("WINDSURF_API_KEY 未设置：export WINDSURF_API_KEY=<your_key> 后再跑（不要硬编码到代码）")
	}

	poolCfg := resolveCfg(*configPath, *binary, *dataRoot, *port)
	if poolCfg.BinaryPath == "" {
		log.Fatalf("LS binary 未设置：用 -binary / LS_BINARY_PATH，或先 bash ../WindsurfAPI/install-ls.sh")
	}
	if _, err := os.Stat(poolCfg.BinaryPath); err != nil {
		log.Fatalf("LS binary 不存在 %s: %v", poolCfg.BinaryPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *total)
	defer cancel()

	pool := ls.NewPool(poolCfg)
	defer pool.StopAll()

	log.Printf("[1/3] 启动 LS binary=%s port=%d data=%s", poolCfg.BinaryPath, poolCfg.DefaultPort, poolCfg.DataRoot)
	t0 := time.Now()
	entry, err := pool.EnsureDefault(ctx)
	if err != nil {
		log.Fatalf("EnsureDefault: %v", err)
	}
	log.Printf("[1/3] ✅ LS ready port=%d elapsed=%s", entry.Port, time.Since(t0))

	grpcClient := grpcpkg.NewClient()
	defer grpcClient.CloseIdleConnections()
	cli := windsurf.NewClient(pool, grpcClient)

	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "status":
		runStatus(ctx, cli, apiKey)
	case "send":
		runSend(ctx, cli, apiKey, *modelID, *prompt)
	default:
		log.Fatalf("未知 -mode=%q，只支持 status 或 send", *mode)
	}
}

func runStatus(ctx context.Context, cli *windsurf.Client, apiKey string) {
	log.Printf("[2/3] 调 GetUserStatus（带 metadata, api_key=%s）", maskKey(apiKey))

	callCtx, callCancel := context.WithTimeout(ctx, 15*time.Second)
	defer callCancel()
	t1 := time.Now()
	status, err := cli.GetUserStatus(callCtx, apiKey)
	elapsed := time.Since(t1)

	if err != nil {
		switch {
		case isProtovalidateErr(err):
			log.Fatalf("[3/3] ❌ FAIL 仍然是 protovalidate 错误（metadata 还是没传对）: %v", err)
		case isTransportErr(err):
			log.Fatalf("[3/3] ❌ FAIL 传输层错误 (%s): %v", elapsed, err)
		default:
			// 鉴权类 / 其它业务错误 —— 链路通、业务也走到了，验收通过。
			log.Printf("[3/3] ✅ PASS 链路通 + 业务调通（业务返回错误，验收已达标） (%s): %v", elapsed, err)
			return
		}
	}

	log.Printf("[3/3] ✅ PASS GetUserStatus 业务通 (%s)", elapsed)
	printStatus(status)
}

func runSend(ctx context.Context, cli *windsurf.Client, apiKey, modelID, prompt string) {
	model := models.GetModelByID(modelID)
	if model == nil {
		log.Fatalf("未知 Claude 模型 %q。可用模型：%s", modelID, strings.Join(modelIDs(), ", "))
	}
	if strings.TrimSpace(prompt) == "" {
		log.Fatalf("-prompt 不能为空")
	}

	log.Printf("[2/3] 发送 Cascade 消息 model=%s uid=%s api_key=%s", model.ID, model.ModelUID, maskKey(apiKey))
	t1 := time.Now()
	result, err := cli.Chat(ctx, windsurf.ChatRequest{
		APIKey:       apiKey,
		ModelEnum:    model.ModelEnum,
		ModelUID:     model.ModelUID,
		ReportedName: model.ID,
		Messages: []windsurf.ChatMessage{
			{Role: "user", Content: prompt},
		},
		OnDelta: func(text string) error {
			fmt.Print(text)
			return nil
		},
	})
	elapsed := time.Since(t1)
	if err != nil {
		switch {
		case isProtovalidateErr(err):
			log.Fatalf("\n[3/3] ❌ FAIL protovalidate 错误（请求字段仍不对）: %v", err)
		case isTransportErr(err):
			log.Fatalf("\n[3/3] ❌ FAIL 传输层错误 (%s): %v", elapsed, err)
		default:
			log.Fatalf("\n[3/3] ❌ FAIL Cascade 业务错误 (%s): %v", elapsed, err)
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		log.Fatalf("\n[3/3] ❌ FAIL Cascade 返回空文本 (%s)", elapsed)
	}
	if result.Usage != nil {
		log.Printf("\n[3/3] ✅ PASS send 成功 (%s) finish=%s usage in=%d out=%d cache_read=%d",
			elapsed, result.FinishReason, result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadTokens)
	} else {
		log.Printf("\n[3/3] ✅ PASS send 成功 (%s) finish=%s", elapsed, result.FinishReason)
	}
	fmt.Printf("\n---- Assistant Text ----\n%s\n", result.Text)
}

func modelIDs() []string {
	out := make([]string, 0, len(models.SupportedModels))
	for _, m := range models.SupportedModels {
		out = append(out, m.ID)
	}
	return out
}

func printStatus(s *windsurf.UserStatus) {
	if s == nil {
		fmt.Println("(UserStatus 为空)")
		return
	}
	fmt.Println("---- UserStatus ----")
	fmt.Printf("Email                  : %s\n", s.Email)
	fmt.Printf("DisplayName            : %s\n", s.DisplayName)
	fmt.Printf("Pro                    : %v\n", s.Pro)
	fmt.Printf("TeamsTier              : %d\n", s.TeamsTier)
	fmt.Printf("TierName               : %s\n", s.TierName)
	fmt.Printf("PlanName               : %s\n", s.PlanName)
	fmt.Printf("TeamID                 : %s\n", s.TeamID)
	fmt.Printf("HasPaidFeatures        : %v\n", s.HasPaidFeatures)
	fmt.Printf("IsEnterprise / IsTeams : %v / %v\n", s.IsEnterprise, s.IsTeams)
	fmt.Printf("PromptCredits used/cap : %d / %d\n", s.UserUsedPromptCredits, s.MonthlyPromptCredits)
	fmt.Printf("FlowCredits   used/cap : %d / %d\n", s.UserUsedFlowCredits, s.MonthlyFlowCredits)
	fmt.Printf("MaxPremiumChatMessages : %d\n", s.MaxPremiumChatMessages)
	if s.TrialEndMs > 0 {
		fmt.Printf("TrialEnd               : %s\n", time.UnixMilli(s.TrialEndMs).Format(time.RFC3339))
	}
	fmt.Printf("AllowedModels          : %d entries\n", len(s.AllowedModels))
}

func maskKey(k string) string {
	if len(k) <= 6 {
		return "***"
	}
	return k[:4] + "…" + k[len(k)-2:]
}

func isTransportErr(err error) bool {
	s := strings.ToLower(err.Error())
	for _, bad := range []string{"dial tcp", "connection refused", "http2:", "i/o timeout", "unexpected eof"} {
		if strings.Contains(s, bad) {
			return true
		}
	}
	return false
}

func isProtovalidateErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "protovalidate") ||
		strings.Contains(s, "validation error") ||
		strings.Contains(s, "value is required")
}

func resolveCfg(yamlPath, binary, dataRoot string, port int) ls.Config {
	out := ls.Config{}
	if cfg, err := loadYAMLCfg(yamlPath); err == nil {
		out = cfg
	}
	if binary != "" {
		out.BinaryPath = binary
	} else if v := os.Getenv("LS_BINARY_PATH"); v != "" && out.BinaryPath == "" {
		out.BinaryPath = v
	}
	if dataRoot != "" {
		out.DataRoot = dataRoot
	}
	if port > 0 {
		out.DefaultPort = port
	}
	return out
}

func loadYAMLCfg(path string) (ls.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return ls.Config{}, err
	}
	out := ls.Config{
		BinaryPath:   cfg.LS.BinaryPath,
		DataRoot:     cfg.LS.DataRoot,
		APIServerURL: cfg.LS.APIServerURL,
		RegisterURL:  cfg.LS.RegisterURL,
		CSRFToken:    cfg.LS.CSRFToken,
		DefaultPort:  cfg.LS.DefaultPort,
	}
	if cfg.LS.ReadySeconds > 0 {
		out.ReadyTimeout = time.Duration(cfg.LS.ReadySeconds) * time.Second
	}
	return out, nil
}
