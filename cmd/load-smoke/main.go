package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type result struct {
	status  int
	latency time.Duration
	err     string
	body    string
	account string
	model   string
	route   string
}

type runOptions struct {
	baseURL    string
	apiKey     string
	model      string
	prompt     string
	scenario   string
	route      string
	stream     bool
	accountIDs []int
	timeout    time.Duration
	payload    map[string]any
}

type debugAccountsResponse struct {
	Accounts []debugAccount `json:"accounts"`
}

type debugAccount struct {
	ID             int               `json:"id"`
	Enabled        bool              `json:"enabled"`
	Banned         bool              `json:"banned"`
	TokenSet       bool              `json:"token_set"`
	QuotaScore     float64           `json:"quota_score"`
	ModelCooldowns map[string]string `json:"model_cooldowns"`
}

func main() {
	baseURL := flag.String("url", "http://127.0.0.1:3456", "WindsurfAPI-Go base URL")
	apiKey := flag.String("api-key", "sk-windsurf-default", "API key")
	model := flag.String("model", "claude-sonnet-4.6", "测试模型")
	modelsFlag := flag.String("models", "", "controlled 模式模型 fallback 列表，逗号分隔；为空时使用 -model")
	concurrency := flag.Int("concurrency", 2, "并发数")
	requests := flag.Int("requests", 10, "总请求数")
	timeout := flag.Duration("timeout", 180*time.Second, "单请求超时")
	prompt := flag.String("prompt", "Reply with exactly: hi", "用户消息")
	scenario := flag.String("scenario", "text", "场景：text / tools / tool-result")
	route := flag.String("route", "chat", "路由：chat / messages / responses")
	stream := flag.Bool("stream", false, "使用 streaming 请求并验证协议结束事件")
	mode := flag.String("mode", "single", "single / controlled")
	accountIDsFlag := flag.String("account-ids", "", "只使用指定账号 ID，逗号分隔；server 只接受 localhost 请求")
	groupSize := flag.Int("group-size", 3, "controlled 模式每组账号数")
	users := flag.Int("users", 3, "controlled 模式模拟用户数")
	rounds := flag.Int("rounds", 2, "controlled 模式每个用户的对话轮数")
	maxGroups := flag.Int("max-groups", 2, "controlled 模式最多尝试账号组数")
	maxRequestsPerModel := flag.Int("max-requests-per-model", 0, "controlled 模式单模型最多真实请求数；0 表示不额外限制")
	flag.Parse()

	if *mode == "controlled" {
		runControlled(*baseURL, *apiKey, splitModels(*modelsFlag, *model), parseAccountIDs(*accountIDsFlag), *groupSize, *users, *rounds, *maxGroups, *maxRequestsPerModel, *timeout)
		return
	}

	opts := runOptions{
		baseURL:    *baseURL,
		apiKey:     *apiKey,
		model:      *model,
		prompt:     *prompt,
		scenario:   *scenario,
		route:      *route,
		stream:     *stream,
		accountIDs: parseAccountIDs(*accountIDsFlag),
		timeout:    *timeout,
	}
	all, total := runBatch(opts, *concurrency, *requests)
	printSummary(all, *model, *scenario, *route, *stream, *concurrency, total)
}

func runBatch(opts runOptions, concurrency, requests int) ([]result, time.Duration) {
	if concurrency <= 0 {
		concurrency = 1
	}
	if requests <= 0 {
		return nil, 0
	}
	jobs := make(chan int)
	results := make(chan result, requests)
	var wg sync.WaitGroup
	client := &http.Client{Timeout: opts.timeout}

	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				results <- call(client, opts)
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)

	var all []result
	for r := range results {
		all = append(all, r)
	}
	return all, time.Since(start)
}

func call(client *http.Client, opts runOptions) result {
	payload, path := opts.payload, ""
	if payload == nil {
		payload, path = buildPayload(opts.model, opts.prompt, opts.scenario, opts.route, opts.stream)
	} else {
		_, path = buildPayload(opts.model, opts.prompt, opts.scenario, opts.route, opts.stream)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, opts.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return result{err: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if len(opts.accountIDs) > 0 {
		req.Header.Set("X-Windsurf-Test-Account-IDs", joinInts(opts.accountIDs))
	}
	t0 := time.Now()
	resp, err := client.Do(req)
	lat := time.Since(t0)
	if err != nil {
		return result{latency: lat, err: err.Error(), model: opts.model, route: opts.route}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	bodyText := string(raw)
	if resp.StatusCode == http.StatusOK && opts.stream {
		if err := validateStreamBody(opts.route, bodyText); err != "" {
			return result{status: resp.StatusCode, latency: lat, body: bodyText, account: resp.Header.Get("X-Windsurf-Account-ID"), err: err, model: opts.model, route: opts.route}
		}
	}
	return result{status: resp.StatusCode, latency: lat, body: bodyText, account: resp.Header.Get("X-Windsurf-Account-ID"), model: opts.model, route: opts.route}
}

func buildPayload(model, prompt, scenario, route string, stream bool) (map[string]any, string) {
	if stream && scenario != "text" && scenario != "tools" {
		scenario = "text"
	}
	switch route {
	case "messages":
		return withStream(buildMessagesPayload(model, prompt, scenario), stream), "/v1/messages"
	case "responses":
		return withStream(buildResponsesPayload(model, prompt, scenario), stream), "/v1/responses"
	default:
		return withStream(buildChatPayload(model, prompt, scenario), stream), "/v1/chat/completions"
	}
}

func withStream(payload map[string]any, stream bool) map[string]any {
	if stream {
		payload["stream"] = true
	}
	return payload
}

func runControlled(baseURL, apiKey string, models []string, requestedIDs []int, groupSize, users, rounds, maxGroups, maxRequestsPerModel int, timeout time.Duration) {
	if groupSize <= 0 {
		groupSize = 3
	}
	if users <= 0 {
		users = groupSize
	}
	if rounds <= 0 {
		rounds = 1
	}
	if maxGroups <= 0 {
		maxGroups = 1
	}
	client := &http.Client{Timeout: timeout}
	accountIDs := requestedIDs
	if len(accountIDs) == 0 {
		ids, err := fetchActiveAccountIDs(client, baseURL, apiKey)
		if err != nil {
			fmt.Printf("failed to fetch accounts: %v\n", err)
			return
		}
		accountIDs = ids
	}
	if len(accountIDs) == 0 {
		fmt.Println("no active accounts available for controlled smoke")
		return
	}

	groups := accountGroups(accountIDs, groupSize, maxGroups)
	fmt.Printf("controlled_smoke models=%v accounts=%v groups=%v users=%d rounds=%d max_requests_per_model=%d\n", models, accountIDs, groups, users, rounds, maxRequestsPerModel)
	totalRequests := 0
	modelRequests := map[string]int{}
	for gi, group := range groups {
		groupOK := false
		fmt.Printf("group_start index=%d accounts=%v\n", gi+1, group)
		for _, model := range models {
			if maxRequestsPerModel > 0 && modelRequests[model] >= maxRequestsPerModel {
				fmt.Printf("model_request_budget_reached model=%s requests=%d\n", model, modelRequests[model])
				continue
			}
			steps := controlledConversationSteps(users, rounds, model)
			remaining := len(steps)
			if maxRequestsPerModel > 0 && modelRequests[model]+remaining > maxRequestsPerModel {
				remaining = maxRequestsPerModel - modelRequests[model]
			}
			if remaining <= 0 {
				continue
			}
			if remaining < len(steps) {
				steps = steps[:remaining]
			}
			opts := runOptions{baseURL: baseURL, apiKey: apiKey, model: model, accountIDs: group, timeout: timeout}
			results := make([]result, 0, len(steps))
			start := time.Now()
			for _, step := range steps {
				opts.route = step.route
				opts.scenario = step.scenario
				opts.stream = step.stream
				opts.prompt = step.prompt
				opts.payload = step.payload
				results = append(results, call(client, opts))
				totalRequests++
				modelRequests[model]++
			}
			printSummary(results, model, "controlled-real-conversation", "mixed", false, minInt(users, len(group)), time.Since(start))
			if allRateLimited(results) {
				fmt.Printf("model_rate_limited model=%s group=%v action=try_next_model_or_group\n", model, group)
				continue
			}
			groupOK = true
			if successRate(results) >= 0.80 {
				fmt.Printf("group_ok index=%d model=%s success_rate=%.1f%%\n", gi+1, model, successRate(results)*100)
				return
			}
			fmt.Printf("group_partial index=%d model=%s success_rate=%.1f%% action=try_next_model\n", gi+1, model, successRate(results)*100)
		}
		if !groupOK {
			fmt.Printf("group_unavailable index=%d accounts=%v action=try_next_group\n", gi+1, group)
		}
	}
}

type controlledScenario struct {
	route    string
	scenario string
	stream   bool
	prompt   string
	payload  map[string]any
}

func controlledConversationSteps(users, rounds int, model string) []controlledScenario {
	var out []controlledScenario
	routes := []string{"chat", "messages", "responses"}
	for u := 1; u <= users; u++ {
		histories := map[string][]string{}
		for r := 1; r <= rounds; r++ {
			route := routes[(u+r-2)%len(routes)]
			stream := r%2 == 0
			prompt := fmt.Sprintf("You are serving user %d in round %d. Reply with exactly: U%d_R%d_OK", u, r, u, r)
			histories[route] = append(histories[route], prompt)
			out = append(out, controlledScenario{
				route:    route,
				scenario: "text",
				stream:   stream,
				prompt:   prompt,
				payload:  buildConversationPayload(model, route, histories[route], stream),
			})
			histories[route] = append(histories[route], fmt.Sprintf("U%d_R%d_OK", u, r))
			if r == rounds {
				out = append(out, controlledScenario{route: route, scenario: "tool-result", prompt: "Return only the provided tool result."})
			}
		}
	}
	return out
}

func buildConversationPayload(model, route string, history []string, stream bool) map[string]any {
	switch route {
	case "messages":
		messages := make([]map[string]string, 0, len(history))
		for i, text := range history {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			messages = append(messages, map[string]string{"role": role, "content": text})
		}
		return withStream(map[string]any{"model": model, "messages": messages}, stream)
	case "responses":
		input := make([]any, 0, len(history))
		for i, text := range history {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			input = append(input, map[string]any{"type": "message", "role": role, "content": text})
		}
		return withStream(map[string]any{"model": model, "input": input}, stream)
	default:
		messages := make([]map[string]string, 0, len(history))
		for i, text := range history {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			messages = append(messages, map[string]string{"role": role, "content": text})
		}
		return withStream(map[string]any{"model": model, "messages": messages}, stream)
	}
}

func fetchActiveAccountIDs(client *http.Client, baseURL, apiKey string) ([]int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/debug/accounts", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("debug accounts status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed debugAccountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	var ids []int
	for _, a := range parsed.Accounts {
		if a.Enabled && !a.Banned && a.TokenSet {
			ids = append(ids, a.ID)
		}
	}
	sort.Ints(ids)
	return ids, nil
}

func accountGroups(ids []int, size, maxGroups int) [][]int {
	var groups [][]int
	for i := 0; i < len(ids) && len(groups) < maxGroups; i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		groups = append(groups, append([]int(nil), ids[i:end]...))
	}
	return groups
}

func splitModels(raw, fallback string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		out = append(out, strings.TrimSpace(fallback))
	}
	return out
}

func parseAccountIDs(raw string) []int {
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func joinInts(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ",")
}

func buildChatPayload(model, prompt, scenario string) map[string]any {
	tool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "echo_text",
			"description": "Echoes text back to the caller.",
			"parameters": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"text": map[string]any{"type": "string"}},
				"required":             []string{"text"},
				"additionalProperties": false,
			},
			"strict": true,
		},
	}
	switch scenario {
	case "tools":
		return map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "Use the echo_text tool exactly once with text set to LOAD_TOOL_OK."},
			},
			"tools":       []any{tool},
			"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "echo_text"}},
		}
	case "tool-result":
		return map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "Use the echo_text tool once, then answer with only the returned text."},
				{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "toolu_load_1", "type": "function", "function": map[string]any{"name": "echo_text", "arguments": `{"text":"LOAD_RESULT_OK"}`}}}},
				{"role": "tool", "tool_call_id": "toolu_load_1", "content": "LOAD_RESULT_OK"},
			},
			"tools": []any{tool},
		}
	default:
		return map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"stream": false,
		}
	}
}

func buildMessagesPayload(model, prompt, scenario string) map[string]any {
	tool := map[string]any{
		"name":        "echo_text",
		"description": "Echoes text back to the caller.",
		"input_schema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
	}
	switch scenario {
	case "tools":
		return map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "Use the echo_text tool exactly once with text set to LOAD_TOOL_OK."},
			},
			"tools":       []any{tool},
			"tool_choice": map[string]any{"type": "tool", "name": "echo_text"},
		}
	case "tool-result":
		return map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "Use the echo_text tool once, then answer with only the returned text."},
				{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "toolu_load_1", "name": "echo_text", "input": map[string]any{"text": "LOAD_RESULT_OK"}}}},
				{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_load_1", "content": "LOAD_RESULT_OK"}}},
			},
			"tools": []any{tool},
		}
	default:
		return map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"stream": false,
		}
	}
}

func buildResponsesPayload(model, prompt, scenario string) map[string]any {
	tool := map[string]any{
		"type":        "function",
		"name":        "echo_text",
		"description": "Echoes text back to the caller.",
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
	}
	switch scenario {
	case "tools":
		return map[string]any{
			"model":       model,
			"input":       []any{map[string]any{"type": "message", "role": "user", "content": "Use the echo_text tool exactly once with text set to LOAD_TOOL_OK."}},
			"tools":       []any{tool},
			"tool_choice": map[string]any{"type": "function", "name": "echo_text"},
		}
	case "tool-result":
		return map[string]any{
			"model": model,
			"input": []any{
				map[string]any{"type": "message", "role": "user", "content": "Use the echo_text tool once, then answer with only the returned text."},
				map[string]any{"type": "function_call", "call_id": "toolu_load_1", "name": "echo_text", "arguments": `{"text":"LOAD_RESULT_OK"}`},
				map[string]any{"type": "function_call_output", "call_id": "toolu_load_1", "output": "LOAD_RESULT_OK"},
			},
			"tools": []any{tool},
		}
	default:
		return map[string]any{"model": model, "input": prompt, "stream": false}
	}
}

func printSummary(results []result, model, scenario, route string, stream bool, concurrency int, total time.Duration) {
	statusCounts := map[int]int{}
	errCounts := map[string]int{}
	accountCounts := map[string]int{}
	var latencies []time.Duration
	success := 0
	for _, r := range results {
		statusCounts[r.status]++
		if r.status == 200 {
			success++
		}
		if r.err != "" {
			errCounts[r.err]++
		} else if r.status != 200 {
			errCounts[extractError(r.body)]++
		}
		if r.latency > 0 {
			latencies = append(latencies, r.latency)
		}
		if r.account != "" {
			accountCounts[r.account]++
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	fmt.Printf("model=%s route=%s scenario=%s stream=%v requests=%d concurrency=%d success=%d success_rate=%.1f%% total=%s\n", model, route, scenario, stream, len(results), concurrency, success, float64(success)*100/float64(len(results)), total.Round(time.Millisecond))
	fmt.Printf("status=%v\n", statusCounts)
	fmt.Printf("accounts=%v\n", accountCounts)
	fmt.Printf("latency p50=%s p95=%s p99=%s\n", percentile(latencies, 0.50), percentile(latencies, 0.95), percentile(latencies, 0.99))
	if len(errCounts) > 0 {
		fmt.Printf("errors:\n")
		for msg, n := range errCounts {
			fmt.Printf("  %d x %s\n", n, msg)
		}
	}
}

func successRate(results []result) float64 {
	if len(results) == 0 {
		return 0
	}
	success := 0
	for _, r := range results {
		if r.status == http.StatusOK && r.err == "" {
			success++
		}
	}
	return float64(success) / float64(len(results))
}

func allRateLimited(results []result) bool {
	if len(results) == 0 {
		return false
	}
	failures := 0
	rateLimited := 0
	for _, r := range results {
		if r.status == http.StatusOK && r.err == "" {
			continue
		}
		failures++
		msg := strings.ToLower(r.err + " " + extractError(r.body))
		if r.status == http.StatusTooManyRequests || strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "cooldown") || strings.Contains(msg, "no available accounts") {
			rateLimited++
		}
	}
	return failures > 0 && failures == rateLimited
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func validateStreamBody(route, body string) string {
	switch route {
	case "messages":
		if !strings.Contains(body, "event: message_stop") {
			return "stream missing Anthropic message_stop"
		}
	case "responses":
		if !strings.Contains(body, "event: response.completed") {
			return "stream missing Responses completed event"
		}
	default:
		if !strings.Contains(body, "data: [DONE]") {
			return "stream missing OpenAI [DONE]"
		}
	}
	return ""
}

func percentile(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	idx := int(float64(len(v)-1) * p)
	return v[idx].Round(time.Millisecond)
}

func extractError(body string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Type + ": " + parsed.Error.Message
	}
	if len(body) > 240 {
		return body[:240]
	}
	return body
}
