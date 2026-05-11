package health

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type Config struct {
	Enabled           bool
	Interval          time.Duration
	Timeout           time.Duration
	MarkInvalidBanned bool
	CheckModelConfigs bool
	ReadyRequireCheck bool
	Model             string
}

type Worker struct {
	cfg Config
	am  *account.Manager
	dc  *direct.Client
	pp  *proxypool.Manager

	mu      sync.RWMutex
	started bool
	ready   bool
	last    account.HealthSummary
}

func NewWorker(cfg Config, am *account.Manager, dc *direct.Client) *Worker {
	return NewWorkerWithProxy(cfg, am, dc, nil)
}

func NewWorkerWithProxy(cfg Config, am *account.Manager, dc *direct.Client, pp *proxypool.Manager) *Worker {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &Worker{cfg: cfg, am: am, dc: dc, pp: pp, ready: !cfg.ReadyRequireCheck}
}

func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	if !w.cfg.Enabled {
		w.setReady(true)
		return
	}
	go w.loop(ctx)
}

func (w *Worker) Ready() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ready
}

func (w *Worker) Snapshot() account.HealthSummary {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.last
}

func (w *Worker) loop(ctx context.Context) {
	w.runOnce(ctx)
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	start := time.Now()
	summary := account.HealthSummary{LastRunAt: start}
	accounts, err := w.am.GetAllAccounts()
	if err != nil {
		summary.LastError = redact.Text(err.Error())
		w.finish(summary, start)
		return
	}
	for i := range accounts {
		a := accounts[i]
		if !a.Enabled || a.Banned || strings.TrimSpace(a.FirebaseToken) == "" {
			continue
		}
		summary.Checked++
		checkCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
		status, retryUntil, modelConfigCount, err := w.checkOne(checkCtx, &a)
		cancel()
		if err != nil {
			if isInvalidToken(err) {
				summary.Invalid++
				if w.cfg.MarkInvalidBanned {
					if markErr := w.am.MarkBanned(a.ID); markErr != nil {
						log.Printf("health account=%d mark_banned_error=%v", a.ID, markErr)
					}
				}
			} else {
				summary.Failed++
				summary.LastError = redact.Text(err.Error())
			}
			log.Printf("health account=%d email=%s status=failed err=%s", a.ID, redact.Text(a.Email), redact.Text(err.Error()))
			continue
		}
		summary.OK++
		tier := TierFromPlan(status.PlanName)
		note := fmt.Sprintf("health ok model=%s checked_at=%s", w.cfg.Model, time.Now().Format(time.RFC3339))
		update := AccountHealthUpdate(tier, status, retryUntil, note)
		update.ModelConfigCount = modelConfigCount
		if err := w.am.UpdateHealthDetails(a.ID, update); err != nil {
			summary.Failed++
			summary.LastError = redact.Text(err.Error())
			log.Printf("health account=%d update_error=%s", a.ID, redact.Text(err.Error()))
			continue
		}
	}
	w.runProxyMaintenance(ctx, accounts)
	w.finish(summary, start)
}

func (w *Worker) runProxyMaintenance(ctx context.Context, accounts []account.Account) {
	if w.pp == nil || !w.pp.AccountBindingEnabled() {
		return
	}
	refs := make([]proxypool.AccountRef, 0, len(accounts))
	for _, a := range accounts {
		refs = append(refs, proxypool.AccountRef{ID: a.ID, Enabled: a.Enabled, Banned: a.Banned, Active: a.Enabled && !a.Banned})
	}
	maintCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()
	if result, err := w.pp.RunMaintenance(maintCtx, refs); err != nil {
		log.Printf("dynamic_proxy_maintenance error=%s result=%v", redact.Text(err.Error()), result)
	} else if planned, _ := result["planned"].(int); planned > 0 {
		log.Printf("dynamic_proxy_maintenance planned=%v processed=%v failed=%v", result["planned"], result["processed"], result["failed"])
	}
}

func (w *Worker) checkOne(ctx context.Context, a *account.Account) (*direct.UserStatus, *time.Time, int, error) {
	proxyURL := w.effectiveProxy(a)
	status, err := w.dc.GetUserStatusWithProxy(ctx, a.FirebaseToken, proxyURL)
	if err != nil {
		return nil, nil, 0, err
	}
	rl, err := w.dc.CheckMessageRateLimitWithProxy(ctx, a.FirebaseToken, proxyURL)
	if err != nil {
		return nil, nil, 0, err
	}
	var retryUntil *time.Time
	if !rl.HasCapacity && rl.RetryAfterMS != nil && *rl.RetryAfterMS > 0 {
		until := time.Now().Add(time.Duration(*rl.RetryAfterMS) * time.Millisecond)
		retryUntil = &until
	}
	modelConfigCount := 0
	if w.cfg.CheckModelConfigs {
		cfgs, err := w.dc.GetCascadeModelConfigsWithProxy(ctx, a.FirebaseToken, proxyURL)
		if err != nil {
			return nil, nil, 0, err
		}
		modelConfigCount = len(cfgs.Configs)
	}
	return status, retryUntil, modelConfigCount, nil
}

func (w *Worker) effectiveProxy(a *account.Account) string {
	if w == nil || w.pp == nil || a == nil {
		if a == nil {
			return ""
		}
		return a.ProxyURL
	}
	return w.pp.EffectiveProxyURL(a.ID, a.ProxyURL)
}

func (w *Worker) finish(summary account.HealthSummary, start time.Time) {
	summary.LastDurationMS = time.Since(start).Milliseconds()
	w.am.RecordHealthSummary(summary)
	w.mu.Lock()
	w.last = summary
	if !w.cfg.ReadyRequireCheck || summary.OK > 0 {
		w.ready = true
	}
	w.mu.Unlock()
	log.Printf("health summary checked=%d ok=%d invalid=%d failed=%d duration_ms=%d", summary.Checked, summary.OK, summary.Invalid, summary.Failed, summary.LastDurationMS)
}

func (w *Worker) setReady(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = v
}

func isInvalidToken(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "invalid devin token") ||
		strings.Contains(msg, "failed to validate devin token") ||
		strings.Contains(msg, "logging out and logging in again") ||
		strings.Contains(msg, "authentication failed")
}

func TierFromPlan(plan string) string {
	plan = strings.ToLower(plan)
	switch {
	case strings.Contains(plan, "pro"),
		strings.Contains(plan, "team"),
		strings.Contains(plan, "enterprise"),
		strings.Contains(plan, "trial"),
		strings.Contains(plan, "individual"),
		strings.Contains(plan, "premium"),
		strings.Contains(plan, "paid"):
		return "pro"
	case strings.Contains(plan, "free"):
		return "free"
	default:
		return "unknown"
	}
}

func AccountHealthUpdate(tier string, status *direct.UserStatus, rateLimitedUntil *time.Time, note string) account.HealthUpdate {
	update := account.HealthUpdate{
		Tier:             tier,
		RateLimitedUntil: rateLimitedUntil,
		HealthCheckedAt:  time.Now(),
		Note:             note,
	}
	if status == nil {
		return update
	}
	update.PlanName = status.PlanName
	update.DailyPercent = status.DailyPercent
	update.WeeklyPercent = status.WeeklyPercent
	update.DailyResetAt = unixTimePtr(status.DailyResetAt)
	update.WeeklyResetAt = unixTimePtr(status.WeeklyResetAt)
	update.PromptLimit = status.Prompt.Limit
	update.PromptUsed = status.Prompt.Used
	update.PromptRemaining = status.Prompt.Remaining
	update.FlexLimit = status.Flex.Limit
	update.FlexUsed = status.Flex.Used
	update.FlexRemaining = status.Flex.Remaining
	update.OverageBalance = status.OverageBalance
	update.PlanStart = status.PlanStart
	update.PlanEnd = status.PlanEnd
	return update
}

func unixTimePtr(raw *int64) *time.Time {
	if raw == nil || *raw <= 0 {
		return nil
	}
	ts := time.Unix(*raw, 0)
	return &ts
}
