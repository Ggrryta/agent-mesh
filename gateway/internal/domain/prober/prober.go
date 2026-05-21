// Package prober 周期性 GET 每个已注册 agent 的 /health，根据结果切换
// 它的 active / inactive 状态。
//
// 并发模型（K8s-native）：
//
//   - N 个 gateway 副本都跑 prober。不走主选，而是在行级别认领工作：
//     每个 tick 里，副本先 UPDATE agents SET last_probed_at=NOW()
//     WHERE agent_id=? AND (probed > 15s ago)，RowsAffected=1 才真的探。
//     抢输的副本本轮跳过这个 agent。
//   - 失败次数在副本内存里累计；连续失败 N 次后把 agent 翻成 inactive。
//     之后任一次探测成功都会翻回 active。
//
// 这样 prober 简单且对副本变化鲁棒：不租约、不选主，只靠 DB 行级认领。
package prober

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Config 收集所有可调参数。零值走合理默认。
type Config struct {
	// Interval 两次 tick 的间隔。默认 15s。
	Interval time.Duration
	// ClaimTTL 控制 last_probed_at 需要过期多久另一个副本才能重新探。
	// 默认 15s：抢到行的副本实际上独占它到下一个 tick。
	ClaimTTL time.Duration
	// HTTPTimeout 每次 GET 的超时。默认 3s。
	HTTPTimeout time.Duration
	// FailureThreshold 连续失败多少次后 flip 成 inactive。默认 3。
	FailureThreshold int
}

func (c *Config) normalize() {
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
	if c.ClaimTTL <= 0 {
		c.ClaimTTL = 15 * time.Second
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 3 * time.Second
	}
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 3
	}
}

// Prober 是轮询器，每个 gateway 进程实例化一个。
type Prober struct {
	db     *sql.DB
	cfg    Config
	log    *zap.Logger
	client *http.Client

	mu   sync.Mutex
	fail map[string]int // 每个 agent 的连续失败计数
}

// New 返回一个就绪的 Prober。agents 参数仅为接口对称保留——恢复走一条
// 窄 UPDATE，prober 独立于 service 层。保留签名是为了将来加 ownership
// 感知的流程时调用方不用改。
func New(db *sql.DB, agents any, cfg Config, log *zap.Logger) *Prober {
	_ = agents // 故意不用；为将来接 service 层预留
	cfg.normalize()
	return &Prober{
		db:     db,
		cfg:    cfg,
		log:    log,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		fail:   make(map[string]int),
	}
}

// Run 阻塞运行直到 ctx 取消。调用方通常放进 goroutine 跑。
func (p *Prober) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick 遍历可探的 agent 并逐个 claim。
// 刻意在一个副本内串行处理；副本之间的并发收益远比副本内并发重要。
func (p *Prober) tick(ctx context.Context) {
	candidates, err := p.listCandidates(ctx)
	if err != nil {
		p.log.Warn("prober: list candidates failed", zap.Error(err))
		return
	}
	for _, c := range candidates {
		claimed, err := p.claim(ctx, c.agentID)
		if err != nil {
			p.log.Warn("prober: claim failed", zap.String("agent_id", c.agentID), zap.Error(err))
			continue
		}
		if !claimed {
			// 另一个副本已经抢到本轮。
			continue
		}
		p.probe(ctx, c)
	}
}

type candidate struct {
	agentID string
	url     string
	status  string
}

// listCandidates 返回有 URL 的 active 或 inactive agent（后者为了能恢复）。
// draining 被跳过——那是运维意图，不要自动复活。
// virtual-user agent 没有 URL，WHERE 条件已经把它排除。
func (p *Prober) listCandidates(ctx context.Context) ([]candidate, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT agent_id, url, status
		FROM agents
		WHERE status IN ('active','inactive')
		  AND url <> ''
		  AND kind = 'normal'
		ORDER BY last_probed_at IS NULL DESC, last_probed_at ASC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]candidate, 0, 16)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.agentID, &c.url, &c.status); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// claim 原子地为本轮认领工作。本副本赢时返回 true。
// WHERE 条件要求"探测时间早于 ClaimTTL 或未探测过"，N 个副本在零协调
// 前提下自然收敛到"每个 agent 每个 tick 窗口被探一次"。
func (p *Prober) claim(ctx context.Context, agentID string) (bool, error) {
	res, err := p.db.ExecContext(ctx, `
		UPDATE agents
		SET last_probed_at = NOW(3)
		WHERE agent_id = ?
		  AND (last_probed_at IS NULL
		       OR last_probed_at < NOW(3) - INTERVAL ? MICROSECOND)`,
		agentID, p.cfg.ClaimTTL.Microseconds(),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// probe 真正 HTTP GET /health，并按结果更新状态。
func (p *Prober) probe(ctx context.Context, c candidate) {
	url := c.url + "/health"
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		p.onFail(ctx, c, fmt.Sprintf("build request: %v", err))
		return
	}
	resp, err := p.client.Do(req)
	if err != nil {
		p.onFail(ctx, c, fmt.Sprintf("http error: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.onOK(ctx, c)
		return
	}
	p.onFail(ctx, c, fmt.Sprintf("status %d", resp.StatusCode))
}

func (p *Prober) onOK(ctx context.Context, c candidate) {
	p.mu.Lock()
	delete(p.fail, c.agentID)
	p.mu.Unlock()
	if c.status == "active" {
		return
	}
	// 恢复：inactive → active。走一条窄 UPDATE，保持 prober 不依赖
	// service 层（service 需要 owner UID，prober 不该管这个）。
	// agent service 会在下次 cache reload 时拿到这个变化。
	if _, err := p.db.ExecContext(ctx,
		"UPDATE agents SET status='active' WHERE agent_id=? AND status='inactive'", c.agentID); err != nil {
		p.log.Warn("prober: flip active failed", zap.String("agent_id", c.agentID), zap.Error(err))
		return
	}
	p.log.Info("prober: recovered", zap.String("agent_id", c.agentID))
}

func (p *Prober) onFail(ctx context.Context, c candidate, reason string) {
	p.mu.Lock()
	p.fail[c.agentID]++
	n := p.fail[c.agentID]
	p.mu.Unlock()
	if n < p.cfg.FailureThreshold {
		return
	}
	if c.status == "inactive" {
		// 已经是 inactive 就不重复 log 了。
		return
	}
	// 直接 UPDATE 翻 inactive（探测无需 owner 校验）。
	if _, err := p.db.ExecContext(ctx,
		"UPDATE agents SET status='inactive' WHERE agent_id=? AND status='active'", c.agentID); err != nil {
		p.log.Warn("prober: flip inactive failed", zap.String("agent_id", c.agentID), zap.Error(err))
		return
	}
	p.log.Warn("prober: flipped to inactive",
		zap.String("agent_id", c.agentID),
		zap.String("reason", reason),
		zap.Int("consecutive_failures", n),
	)
}
