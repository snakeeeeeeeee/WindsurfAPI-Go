# WindsurfAPI-Go 编译修复计划

## 现状
项目骨架已搭建，但 handler 层（chat.go, middleware.go, models.go）与其他层（main.go, account.go, config.go）之间存在 6 处严重不一致，导致无法编译。

## 修复步骤

### 1. 修复 handler 文件的 import 路径
- `chat.go` 和 `middleware.go` 使用了错误的 `windsurf-api-go/internal/...`
- 需改为 `github.com/zhangyu/windsurfapi-go/internal/...`

### 2. 重写 `middleware.go` 匹配 main.go 的调用约定
- main.go 调用 `handler.AuthMiddleware(cfg.Server.APIKeys)` 返回 `func(http.Handler) http.Handler`
- 当前 middleware.go 签名完全不同，需重写为接受 `[]string` 返回中间件包装函数
- 删除对不存在的 `cfg.AuthToken` 和 `am.GetByToken()` 的引用

### 3. 修复 `chat.go`
- 函数名 `ChatHandler` → `ChatCompletionsHandler`（匹配 main.go 的调用）
- 返回类型改为 `http.HandlerFunc`（匹配 main.go 用法）
- `acc.Token` → `acc.FirebaseToken`
- `acc.Name` → `acc.Email`  
- `cfg.WindsurfBaseURL` → `cfg.Windsurf.BaseURL`
- `am.MarkAccountError(acc.Name)` → `am.SetCooldown(acc.ID, time.Now().Add(5*time.Minute))`

### 4. 修复 `handler/models.go`
- 使用 `internal/models` 包的 `ToOpenAIModelList()` 替代硬编码模型列表

### 5. 确保 `account.Manager` 有 `SelectAccount()` 方法
- ✅ 已存在，无需修改

### 6. 验证编译
- `go build ./...` 确认零错误
- `go vet ./...` 确认无警告

## 不改动的文件
- `go.mod` / `go.sum` - 无需变更
- `internal/config/config.go` - 结构正确
- `internal/account/account.go` - 接口正确
- `internal/store/sqlite.go` - 实现正确
- `internal/store/redis.go` - 实现正确
- `internal/models/models.go` - 实现正确
- `cmd/server/main.go` - 设计正确，是 handler 层需要适配它
