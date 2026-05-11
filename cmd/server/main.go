package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/handler"
	"github.com/zhangyu/windsurfapi-go/internal/health"
	"github.com/zhangyu/windsurfapi-go/internal/ls"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	runtimeconfig "github.com/zhangyu/windsurfapi-go/internal/runtimeconfig"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

func main() {
	configPath := flag.String("config", "configs/default.yaml", "配置文件路径")
	flag.Parse()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置加载完成，端口: %d", cfg.Server.Port)

	sqliteStore, err := store.NewSQLiteStore(cfg.SQLite.Path)
	if err != nil {
		log.Fatalf("初始化 SQLite 失败: %v", err)
	}
	defer sqliteStore.Close()
	log.Printf("SQLite 初始化完成: %s", cfg.SQLite.Path)
	handler.ConfigureRequestStatsDB(sqliteStore.DB)

	redisStore, err := store.NewRedisStore(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Printf("⚠️  Redis 连接失败 (非致命): %v", err)
	} else {
		defer redisStore.Close()
		log.Printf("Redis 连接成功: %s", cfg.Redis.Addr)
	}

	accountMgr := account.NewManager(sqliteStore)
	accountMgr.SetMaxInflightPerAccount(cfg.Scheduler.MaxInflightPerAccount)
	modelAccessMgr := modelaccess.NewManager(sqliteStore)
	runtimeConfigMgr := runtimeconfig.NewManager(cfg)
	if cfg.Scheduler.RedisEnabled {
		if redisStore == nil {
			if cfg.Scheduler.RedisFailClosed {
				log.Fatalf("scheduler.redis_enabled=true and redis_fail_closed=true but Redis is unavailable; refusing to start")
			}
			log.Printf("⚠️  scheduler.redis_enabled=true but Redis is unavailable; falling back to single-process scheduler")
		} else {
			accountMgr.SetCoordinator(account.NewRedisCoordinator(redisStore.Client, account.RedisCoordinatorConfig{
				MaxInflightPerAccount: cfg.Scheduler.MaxInflightPerAccount,
				ReservationTTL:        time.Duration(cfg.Scheduler.ReservationTTLSeconds) * time.Second,
			}))
			log.Printf("Redis scheduler coordinator enabled max_inflight_per_account=%d", cfg.Scheduler.MaxInflightPerAccount)
		}
	}

	// Direct-only 是默认生产聊天后端。LS pool 只保留给 legacy smoke/debug，
	// 不在 /v1/chat/completions 主链路启动或依赖本地 language_server。
	poolCfg := ls.Config{
		BinaryPath:   cfg.LS.BinaryPath,
		DataRoot:     cfg.LS.DataRoot,
		APIServerURL: cfg.LS.APIServerURL,
		RegisterURL:  cfg.LS.RegisterURL,
		CSRFToken:    cfg.LS.CSRFToken,
		DefaultPort:  cfg.LS.DefaultPort,
		MaxInstances: cfg.LS.MaxInstances,
	}
	if cfg.LS.ReadySeconds > 0 {
		poolCfg.ReadyTimeout = time.Duration(cfg.LS.ReadySeconds) * time.Second
	}
	lsPool := ls.NewPool(poolCfg)
	defer lsPool.StopAll()
	if cfg.LS.BinaryPath != "" {
		log.Printf("Legacy LS binary configured for debug/smoke only: %s", cfg.LS.BinaryPath)
	}

	directOpts := []direct.Option{direct.WithTimeout(time.Duration(cfg.Direct.TimeoutSeconds) * time.Second)}
	if len(cfg.Direct.Hosts) > 0 {
		directOpts = append(directOpts, direct.WithHosts(cfg.Direct.Hosts))
	}
	if cfg.Proxy.AllowPrivate {
		directOpts = append(directOpts, direct.WithAllowPrivateProxy(true))
	}
	if cfg.Proxy.Default != "" {
		directOpts = append(directOpts, direct.WithDefaultProxyURL(cfg.Proxy.Default))
	}
	if cfg.Direct.NativeChatPrompts {
		directOpts = append(directOpts, direct.WithNativeChatPrompts(true))
		log.Printf("Direct native chat_message_prompts experiment enabled; keep disabled unless real upstream smoke has validated multi-turn/tool history semantics")
	}
	directClient := direct.NewClient(directOpts...)
	proxyMgr := proxypool.NewManager(proxypool.Config{
		Default:           cfg.Proxy.Default,
		Dynamic:           cfg.Proxy.Dynamic,
		RotateOnError:     cfg.Proxy.RotateOnError,
		TestURL:           cfg.Proxy.TestURL,
		Cooldown:          time.Duration(cfg.Proxy.CooldownSeconds) * time.Second,
		DB:                sqliteStore.DB,
		AllowPrivate:      cfg.Proxy.AllowPrivate,
		AccountBinding:    cfg.Proxy.AccountBinding,
		AutoBindNew:       cfg.Proxy.AutoBindNew,
		RenewBefore:       time.Duration(cfg.Proxy.RenewBeforeMS) * time.Millisecond,
		MaxBindRetries:    cfg.Proxy.MaxBindRetries,
		WorkerInterval:    time.Duration(cfg.Proxy.WorkerIntervalMS) * time.Millisecond,
		WorkerBatchSize:   cfg.Proxy.WorkerBatchSize,
		WorkerConcurrency: cfg.Proxy.WorkerConcurrency,
		Provider:          cfg.Proxy.Provider,
		Protocol:          cfg.Proxy.Protocol,
		Host:              cfg.Proxy.Host,
		Port:              cfg.Proxy.Port,
		UsernameTemplate:  cfg.Proxy.UsernameTemplate,
		Password:          cfg.Proxy.Password,
		Region:            cfg.Proxy.Region,
		State:             cfg.Proxy.State,
		TTLMinutes:        cfg.Proxy.TTLMinutes,
	})
	reusePool := reusepool.NewPool()
	healthWorker := health.NewWorkerWithProxy(health.Config{
		Enabled:           cfg.Health.Enabled,
		Interval:          time.Duration(cfg.Health.IntervalSeconds) * time.Second,
		Timeout:           time.Duration(cfg.Health.TimeoutSeconds) * time.Second,
		MarkInvalidBanned: cfg.Health.MarkInvalidBanned,
		CheckModelConfigs: cfg.Health.CheckModelConfigs,
		ReadyRequireCheck: cfg.Health.ReadyRequireCheck,
		Model:             cfg.Health.Model,
	}, accountMgr, directClient, proxyMgr)
	healthWorker.Start(ctx)

	mux := http.NewServeMux()
	auth := handler.AuthMiddlewareFunc(runtimeConfigMgr.APIKeys)
	dashboardAuth := handler.DashboardAuthMiddlewareFunc(runtimeConfigMgr.APIKeys, runtimeConfigMgr.DashboardPassword)

	mux.Handle("/v1/models", auth(handler.ModelsHandler(modelAccessMgr)))
	mux.Handle("/v1/chat/completions", auth(handler.ChatCompletionsHandler(cfg, accountMgr, directClient, reusePool, modelAccessMgr, proxyMgr)))
	mux.Handle("/v1/messages", auth(handler.MessagesHandler(accountMgr, directClient, reusePool, modelAccessMgr, proxyMgr)))
	mux.Handle("/v1/responses", auth(handler.ResponsesHandler(accountMgr, directClient, reusePool, modelAccessMgr, proxyMgr)))
	mux.Handle("/v1/response", auth(handler.ResponsesHandler(accountMgr, directClient, reusePool, modelAccessMgr, proxyMgr)))
	mux.Handle("/auth/status", dashboardAuth(handler.AuthStatusHandler(accountMgr)))
	mux.Handle("/auth/login", dashboardAuth(handler.AuthLoginHandler(accountMgr)))
	mux.Handle("/auth/accounts", dashboardAuth(handler.AuthAccountsHandler(accountMgr)))
	mux.Handle("/auth/accounts/", dashboardAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models") {
			handler.AuthAccountModelsHandler(accountMgr).ServeHTTP(w, r)
			return
		}
		handler.AuthAccountByIDHandler(accountMgr).ServeHTTP(w, r)
	})))
	mux.Handle("/auth/models/", dashboardAuth(handler.AuthModelAccessHandler(modelAccessMgr)))
	mux.Handle("/dashboard", handler.DashboardHandler())
	mux.Handle("/dashboard/", handler.DashboardHandler())
	mux.Handle("/dashboard/api/", dashboardAuth(handler.DashboardAPIHandler(accountMgr, modelAccessMgr, runtimeConfigMgr, directClient, reusePool, lsPool, proxyMgr)))
	mux.Handle("/debug/accounts", auth(handler.DebugAccountsHandler(accountMgr)))
	mux.Handle("/debug/ls", auth(handler.DebugLSHandler(lsPool)))
	mux.Handle("/debug/direct", auth(handler.DebugDirectHandler(directClient)))
	mux.Handle("/debug/scheduler", auth(handler.DebugSchedulerHandler(accountMgr, reusePool)))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !healthWorker.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"WindsurfAPI-Go","version":"0.1.0"}`))
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{Addr: addr, Handler: handler.CORSMiddleware(handler.MaxBodyMiddlewareFunc(runtimeConfigMgr.MaxRequestBodyBytes)(mux))}

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("收到关闭信号，正在平滑关闭...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("平滑关闭失败，强制关闭: %v", err)
			_ = server.Close()
		}
	}()

	log.Printf("🚀 WindsurfAPI-Go 启动成功 http://localhost%s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务器错误: %v", err)
	}
}
