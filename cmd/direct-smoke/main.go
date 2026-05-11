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
	"github.com/zhangyu/windsurfapi-go/internal/models"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "配置文件路径；默认优先 configs/default.yaml，缺失时回退 configs/default.example.yaml")
	accountID := flag.Int("account", 0, "指定账号 id；0 表示选择第一个 enabled 账号")
	modelID := flag.String("model", "claude-sonnet-4.6", "Cascade 探测模型")
	timeout := flag.Duration("timeout", 45*time.Second, "每个云端 direct RPC 超时")
	probeCascade := flag.Bool("probe-cascade", false, "实验性探测云端 Cascade Start/Send/Poll；会消耗一次消息额度")
	probeAPIChat := flag.Bool("probe-api-chat", false, "实验性探测云端 ApiServerService/GetChatMessage；会消耗一次消息额度")
	probeTools := flag.Bool("probe-tools", false, "实验性探测 direct ApiServerService/GetChatMessage native tool_calls；会消耗一次消息额度")
	probeChatTools := flag.Bool("probe-chat-tools", false, "探测生产 direct.Chat 工具链；Opus 4.7 默认走 text tool emulation，会消耗一次消息额度")
	nativePrompts := flag.Bool("native-chat-prompts", false, "实验性在 direct Chat 里发送多条 chat_message_prompts；会消耗额度且默认关闭")
	apiChatRequestType := flag.Uint64("api-chat-request-type", 5, "GetChatMessage request_type；默认 5=CASCADE")
	apiChatPrompt := flag.String("api-chat-prompt", "Reply with exactly: hi", "GetChatMessage 探测 prompt")
	dumpFrames := flag.Bool("dump-api-chat-frames", false, "打印 GetChatMessage raw gRPC frame 字段摘要")
	rawGRPC := flag.Bool("raw-grpc", false, "Cascade 探测使用 application/grpc，而不是 Connect proto；GetChatMessage 总是使用 gRPC")
	hosts := flag.String("hosts", "", "逗号分隔云端 host；默认 server.codeium.com,server.self-serve.windsurf.com")
	flag.Parse()

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
	acct, err := chooseAccount(mgr, *accountID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("account=%d email=%s token_len=%d\n", acct.ID, acct.Email, len(acct.FirebaseToken))

	opts := []direct.Option{direct.WithTimeout(*timeout), direct.WithRawGRPC(*rawGRPC)}
	if parsed := splitHosts(*hosts); len(parsed) > 0 {
		opts = append(opts, direct.WithHosts(parsed))
	}
	if *nativePrompts {
		opts = append(opts, direct.WithNativeChatPrompts(true))
	}
	client := direct.NewClient(opts...)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	status, err := client.GetUserStatusWithProxy(ctx, acct.FirebaseToken, acct.ProxyURL)
	if err != nil {
		log.Fatalf("direct GetUserStatus failed: %v", err)
	}
	fmt.Printf("GetUserStatus ok plan=%s daily=%s weekly=%s prompt_remaining=%s\n",
		status.PlanName, pct(status.DailyPercent), pct(status.WeeklyPercent), num(status.Prompt.Remaining))

	ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rl, err := client.CheckMessageRateLimitWithProxy(ctx, acct.FirebaseToken, acct.ProxyURL)
	if err != nil {
		log.Printf("CheckMessageRateLimit failed: %v", err)
	} else {
		fmt.Printf("CheckMessageRateLimit ok has_capacity=%v remaining=%d max=%d retry_after_ms=%s\n",
			rl.HasCapacity, rl.MessagesRemaining, rl.MaxMessages, int64p(rl.RetryAfterMS))
	}

	ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cfgs, err := client.GetCascadeModelConfigsWithProxy(ctx, acct.FirebaseToken, acct.ProxyURL)
	if err != nil {
		log.Printf("GetCascadeModelConfigs failed: %v", err)
	} else {
		fmt.Printf("GetCascadeModelConfigs ok configs=%d sorts=%d default_override=%v\n",
			len(cfgs.Configs), len(cfgs.Sorts), cfgs.DefaultOverride != nil)
	}

	if !*probeCascade && !*probeAPIChat && !*probeTools && !*probeChatTools {
		fmt.Println("Chat direct probes skipped. Add -probe-cascade, -probe-api-chat, -probe-tools, or -probe-chat-tools to test quota-consuming chat paths without LS.")
		return
	}
	model := models.GetModelByID(*modelID)
	if model == nil {
		log.Fatalf("unknown model: %s", *modelID)
	}
	if *probeChatTools {
		ctx, cancel = context.WithTimeout(context.Background(), *timeout+60*time.Second)
		defer cancel()
		tools := []direct.ToolDefinition{{
			Name:        "echo_text",
			Description: "Echoes the provided text back to the caller.",
			SchemaJSON:  `{"type":"object","properties":{"text":{"type":"string","description":"Text to echo"}},"required":["text"],"additionalProperties":false}`,
			Strict:      true,
		}}
		choice := &direct.ToolChoice{ToolName: "echo_text"}
		result, err := client.Chat(ctx, direct.ChatRequest{
			APIKey:     acct.FirebaseToken,
			ProxyURL:   acct.ProxyURL,
			Model:      model,
			Messages:   []windsurf.ChatMessage{{Role: "user", Content: "Use the echo_text tool exactly once with text set to HELLO_FROM_DIRECT_TOOL."}},
			Tools:      tools,
			ToolChoice: choice,
		})
		if err != nil {
			fmt.Printf("DirectChatTools failed model=%s tool_mode=%s elapsed<=%s err=%q\n", model.ID, direct.ToolModeForRequest(model, tools, choice, nil), *timeout+60*time.Second, err.Error())
		} else {
			fmt.Printf("DirectChatTools ok model=%s tool_mode=%s text=%q thinking=%q tool_calls=%d finish=%s\n",
				model.ID, direct.ToolModeForRequest(model, tools, choice, nil), truncate(result.Text, 120), truncate(result.Thinking, 80), len(result.ToolCalls), result.FinishReason)
			for i, call := range result.ToolCalls {
				fmt.Printf("tool_call[%d] id=%s name=%s args=%s\n", i, call.ID, call.Name, truncate(call.ArgumentsJSON, 220))
			}
		}
		if !*probeAPIChat && !*probeTools && !*probeCascade {
			return
		}
	}
	if *probeAPIChat || *probeTools {
		ctx, cancel = context.WithTimeout(context.Background(), *timeout+60*time.Second)
		defer cancel()
		prompt := *apiChatPrompt
		var tools []direct.ToolDefinition
		var choice *direct.ToolChoice
		if *probeTools {
			prompt = "Use the echo_text tool exactly once with text set to HELLO_FROM_DIRECT_TOOL."
			tools = []direct.ToolDefinition{{
				Name:        "echo_text",
				Description: "Echoes the provided text back to the caller.",
				SchemaJSON:  `{"type":"object","properties":{"text":{"type":"string","description":"Text to echo"}},"required":["text"],"additionalProperties":false}`,
				Strict:      true,
			}}
			choice = &direct.ToolChoice{ToolName: "echo_text"}
		}
		res := client.ProbeAPIChatWithTools(ctx, acct.FirebaseToken, model, prompt, *apiChatRequestType, tools, choice)
		if res.OK() {
			fmt.Printf("ProbeAPIChat ok host=%s protocol=%s frames=%d actual_model=%s elapsed=%s text=%q thinking=%q tool_calls=%d\n",
				res.Host, res.Protocol, res.FrameCount, res.ActualModel, res.Elapsed.Round(time.Millisecond),
				truncate(res.Assistant, 120), truncate(res.Thinking, 80), len(res.ToolCalls))
			for i, call := range res.ToolCalls {
				fmt.Printf("tool_call[%d] id=%s name=%s args=%s\n", i, call.ID, call.Name, truncate(call.ArgumentsJSON, 220))
			}
		} else {
			fmt.Printf("ProbeAPIChat failed host=%s protocol=%s stage=%s frames=%d actual_model=%s elapsed=%s err=%q raw=%s\n",
				res.Host, res.Protocol, res.Stage, res.FrameCount, res.ActualModel, res.Elapsed.Round(time.Millisecond),
				res.Error(), truncate(res.RawResponse, 180))
			for i, call := range res.ToolCalls {
				fmt.Printf("tool_call[%d] id=%s name=%s args=%s\n", i, call.ID, call.Name, truncate(call.ArgumentsJSON, 220))
			}
		}
		if *dumpFrames {
			for i, summary := range res.FrameSummary {
				fmt.Printf("frame[%d] %s\n", i, summary)
			}
		}
		if !*probeCascade {
			return
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), *timeout+60*time.Second)
	defer cancel()
	res := client.ProbeCascade(ctx, acct.FirebaseToken, model, "Reply with exactly: hi")
	if res.OK() {
		fmt.Printf("ProbeCascade ok host=%s protocol=%s cascade=%s elapsed=%s text=%q\n",
			res.Host, res.Protocol, res.CascadeID, res.Elapsed.Round(time.Millisecond), truncate(res.Assistant, 120))
		return
	}
	fmt.Printf("ProbeCascade failed host=%s protocol=%s stage=%s cascade=%s elapsed=%s err=%q raw=%s\n",
		res.Host, res.Protocol, res.Stage, res.CascadeID, res.Elapsed.Round(time.Millisecond), res.Error(), truncate(res.RawResponse, 180))
}

func chooseAccount(mgr *account.Manager, id int) (*account.Account, error) {
	if id > 0 {
		a, err := mgr.GetAccount(id)
		if err != nil {
			return nil, err
		}
		if a == nil {
			return nil, fmt.Errorf("account %d not found", id)
		}
		if strings.TrimSpace(a.FirebaseToken) == "" {
			return nil, fmt.Errorf("account %d has empty token", id)
		}
		return a, nil
	}
	accounts, err := mgr.GetEnabledAccounts()
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if strings.TrimSpace(accounts[i].FirebaseToken) != "" {
			return &accounts[i], nil
		}
	}
	return nil, fmt.Errorf("no enabled account with token")
}

func splitHosts(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if h := strings.TrimSpace(part); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func pct(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f%%", *v)
}

func num(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", *v)
}

func int64p(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
