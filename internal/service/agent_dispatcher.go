package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"
	"agent-gateway/pkg/ratelimit"

	"go.uber.org/zap"
	"gorm.io/datatypes"
)

// A2AMessagePart 参考 A2A spec,兼容 text/data/file 三种
type A2AMessagePart struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	File json.RawMessage `json:"file,omitempty"`
}

// SendMessageInput 调用方 → dispatcher 的通用入参
type SendMessageInput struct {
	Sender       string           // 发送方 agent_id
	Target       string           // 目标 agent_id
	TaskID       string           // 为空 = 新建 task
	Title        string           // 新建 task 时用
	MessageID    string           // 发送方生成的 UUID
	Parts        []A2AMessagePart // A2A 消息内容
	CallerAppID  string           // 发送方所属账号(用于 per-account 限流,可空)
}

// SendMessageResult 返回结果
type SendMessageResult struct {
	TaskID     string `json:"task_id"`
	Seq        int    `json:"seq"`
	MessageID  string `json:"message_id"`
	IsNewTask  bool   `json:"is_new_task"`
}

var (
	// ErrNotFriend 双方非好友
	ErrNotFriend = errors.New("not friend")
	// ErrAgentOffline 目标离线
	ErrAgentOffline = errors.New("agent offline")
	// ErrAgentNotFound target agent 不存在
	ErrAgentNotFound = errors.New("agent not found")
	// ErrRateLimited 触发速率限制
	ErrRateLimited = errors.New("rate limit exceeded")
)

// AgentDispatcher 消息投递中枢
//
//	1. 检查 sender 存在且 active
//	2. 检查 target 存在且 active
//	3. 检查好友关系
//	4. 对 pull 模式 agent:检查在线(OnlineRegistry)
//	5. 创建/追加 task_messages
//	6. 通过 InboxHub 推送给 target
//	7. 通过 MonitorHub 推送给 Web UI 监控流(如果配置了)
type AgentDispatcher struct {
	agentRepo   *repo.AgentRepo
	friendRepo  *repo.FriendshipRepo
	taskRepo    *repo.TaskV2Repo
	onlineReg   *OnlineRegistry
	hub         *InboxHub
	monitorHub  *MonitorHub // 可选,nil 表示不推监控流
	pushInvoker *A2AInvoker // 保留 push 模式兼容(本期 pull 优先)
	limiter     ratelimit.Limiter // 可选,nil 表示不限流(测试/e2e 时可关)
	limitCfg    RateLimitConfig
}

// RateLimitConfig 控制 SendMessage 的速率限制策略
// 三层 key,任一层触发都会拒绝
type RateLimitConfig struct {
	// 每个 sender agent 的限流:windows 秒内最多 N 条
	PerSenderQPS     int // 每秒最多几条(0=不限)
	PerSenderWindow  int // 秒数,默认 1

	// 每对 (sender, target) 的限流:防 A↔B 无限往返
	PerPairQPS    int // windows 秒内最多 N 条(0=不限)
	PerPairWindow int // 秒数,默认 10

	// 每账号的限流:防单账号全局刷爆
	PerAccountQPS    int // windows 秒内最多 N 条(0=不限)
	PerAccountWindow int // 秒数,默认 60
}

// DefaultRateLimitConfig 小团队内网默认值
// 实际生产可通过 config.yaml 或 env 覆盖
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		PerSenderQPS:     5,  // 每个 sender 1 秒最多 5 条
		PerSenderWindow:  1,
		PerPairQPS:       20, // 每对 agent 10 秒最多 20 条
		PerPairWindow:    10,
		PerAccountQPS:    200, // 每账号 1 分钟最多 200 条
		PerAccountWindow: 60,
	}
}

func NewAgentDispatcher(
	agentRepo *repo.AgentRepo,
	friendRepo *repo.FriendshipRepo,
	taskRepo *repo.TaskV2Repo,
	onlineReg *OnlineRegistry,
	hub *InboxHub,
	pushInvoker *A2AInvoker,
) *AgentDispatcher {
	return &AgentDispatcher{
		agentRepo:   agentRepo,
		friendRepo:  friendRepo,
		taskRepo:    taskRepo,
		onlineReg:   onlineReg,
		hub:         hub,
		pushInvoker: pushInvoker,
	}
}

// SetMonitorHub 注入 MonitorHub。在 NewAgentDispatcher 之后链式调用。
// 分开设置是为了不破坏已有 constructor 签名(其他调用方如 minigwlib 不受影响)。
func (d *AgentDispatcher) SetMonitorHub(h *MonitorHub) {
	d.monitorHub = h
}

// SetRateLimiter 注入限流器和策略。nil limiter 表示关闭限流(测试/e2e)
func (d *AgentDispatcher) SetRateLimiter(l ratelimit.Limiter, cfg RateLimitConfig) {
	d.limiter = l
	d.limitCfg = cfg
}

// checkRateLimit 三层限流校验:per-sender / per-pair / per-account
// 任一层触发都返回 ErrRateLimited。callerAppID 可以为空(向后兼容)。
func (d *AgentDispatcher) checkRateLimit(ctx context.Context, senderAgent, targetAgent, callerAppID string) error {
	if d.limiter == nil {
		return nil
	}
	cfg := d.limitCfg

	// 层 1: per-sender
	if cfg.PerSenderQPS > 0 {
		key := fmt.Sprintf("rl:msg:sender:%s", senderAgent)
		if err := d.limiter.Check(ctx, key, cfg.PerSenderQPS); err != nil {
			logger.Warn("rate limit: per-sender triggered",
				zap.String("sender", senderAgent), zap.Error(err))
			return ErrRateLimited
		}
	}

	// 层 2: per-pair(有序化 key,避免 A→B 和 B→A 分别计数)
	if cfg.PerPairQPS > 0 {
		a, b := senderAgent, targetAgent
		if a > b {
			a, b = b, a
		}
		key := fmt.Sprintf("rl:msg:pair:%s:%s", a, b)
		if err := d.limiter.Check(ctx, key, cfg.PerPairQPS); err != nil {
			logger.Warn("rate limit: per-pair triggered",
				zap.String("sender", senderAgent), zap.String("target", targetAgent),
				zap.Error(err))
			return ErrRateLimited
		}
	}

	// 层 3: per-account
	if cfg.PerAccountQPS > 0 && callerAppID != "" {
		key := fmt.Sprintf("rl:msg:account:%s", callerAppID)
		if err := d.limiter.Check(ctx, key, cfg.PerAccountQPS); err != nil {
			logger.Warn("rate limit: per-account triggered",
				zap.String("app_id", callerAppID), zap.Error(err))
			return ErrRateLimited
		}
	}

	return nil
}

// SendMessage 统一发送入口
func (d *AgentDispatcher) SendMessage(ctx context.Context, in SendMessageInput) (*SendMessageResult, error) {
	// 0. 速率限制(最早拒绝,避免无谓的 DB/Redis 查询)
	if err := d.checkRateLimit(ctx, in.Sender, in.Target, in.CallerAppID); err != nil {
		return nil, err
	}

	// 1. sender / target agent 存在性
	targetAgent, err := d.agentRepo.GetByAgentID(ctx, in.Target)
	if err != nil {
		if errors.Is(err, repo.ErrAgentNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}

	// 2. 好友关系
	isFriend, err := d.friendRepo.IsFriend(ctx, in.Sender, in.Target)
	if err != nil {
		return nil, err
	}
	if !isFriend {
		return nil, ErrNotFriend
	}

	// 3. 目标在线(pull 模式)
	if targetAgent.DeliveryMode == model.DeliveryModePull {
		online, err := d.onlineReg.IsOnline(ctx, in.Target)
		if err != nil {
			return nil, err
		}
		if !online {
			return nil, ErrAgentOffline
		}
	}

	// 4. task 创建或追加
	partsJSON, err := json.Marshal(in.Parts)
	if err != nil {
		return nil, err
	}
	content := datatypes.JSON(partsJSON)

	result := &SendMessageResult{MessageID: in.MessageID}
	var createdTask bool

	if in.TaskID == "" {
		// 新建 task
		t, err := d.taskRepo.Create(ctx, repo.CreateTaskParams{
			TaskID:         generateTaskID(),
			Title:          in.Title,
			CreatorAgentID: in.Sender,
			Members:        []string{in.Sender, in.Target},
			InitialMessage: &repo.InitialMessage{
				MessageID: in.MessageID,
				Content:   content,
			},
		})
		if err != nil {
			return nil, err
		}
		result.TaskID = t.TaskID
		result.Seq = 0
		result.IsNewTask = true
		createdTask = true
	} else {
		// 已有 task,校验 sender 是 member
		ok, err := d.taskRepo.IsMember(ctx, in.TaskID, in.Sender)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, repo.ErrTaskV2NotFound
		}
		msg, err := d.taskRepo.AppendMessage(ctx, in.TaskID, in.Sender, in.MessageID, content)
		if err != nil {
			return nil, err
		}
		result.TaskID = in.TaskID
		result.Seq = msg.Seq
	}

	// 5. 推送给 target
	eventData := map[string]any{
		"task_id":    result.TaskID,
		"title":      in.Title,
		"seq":        result.Seq,
		"message_id": in.MessageID,
		"sender":     in.Sender,
		"parts":      in.Parts,
	}
	if createdTask {
		delivered := d.hub.Publish(in.Target, InboxEventTaskCreated, eventData)
		logger.Info("dispatcher publish task_created",
			zap.String("target", in.Target), zap.String("task_id", result.TaskID),
			zap.Bool("delivered", delivered))
	}
	delivered := d.hub.Publish(in.Target, InboxEventTaskMessage, eventData)
	logger.Info("dispatcher publish task_message",
		zap.String("target", in.Target), zap.String("task_id", result.TaskID),
		zap.Int("seq", result.Seq), zap.Bool("delivered", delivered))

	// 6. 同时推到监控流(Web UI 订阅者)
	if d.monitorHub != nil {
		members := []string{in.Sender, in.Target}
		if createdTask {
			d.monitorHub.PublishTaskEvent(members, &MonitorEvent{
				Kind: MonitorEventTaskCreated,
				Data: eventData,
			})
		}
		d.monitorHub.PublishTaskEvent(members, &MonitorEvent{
			Kind: MonitorEventMessage,
			Data: eventData,
		})
	}

	return result, nil
}

// CloseTask 关闭 task,广播 task_closed 事件
func (d *AgentDispatcher) CloseTask(ctx context.Context, taskID, callerAgentID string) error {
	ok, err := d.taskRepo.IsMember(ctx, taskID, callerAgentID)
	if err != nil {
		return err
	}
	if !ok {
		return repo.ErrTaskV2NotFound
	}
	members, err := d.taskRepo.ListMembers(ctx, taskID)
	if err != nil {
		return err
	}
	if err := d.taskRepo.Close(ctx, taskID, model.TaskV2StatusClosed); err != nil {
		return err
	}
	for _, m := range members {
		if m.AgentID == callerAgentID {
			continue
		}
		d.hub.Publish(m.AgentID, InboxEventTaskClosed, map[string]any{
			"task_id":   taskID,
			"closed_by": callerAgentID,
		})
	}
	// 同时推到监控流
	if d.monitorHub != nil {
		memberIDs := make([]string, 0, len(members))
		for _, m := range members {
			memberIDs = append(memberIDs, m.AgentID)
		}
		d.monitorHub.PublishTaskEvent(memberIDs, &MonitorEvent{
			Kind: MonitorEventTaskClosed,
			Data: map[string]any{
				"task_id":   taskID,
				"closed_by": callerAgentID,
			},
		})
	}
	return nil
}

// generateTaskID 简洁的 task_id 生成器。使用 UUID,但项目中多数地方直接用 google/uuid。
// 这里为了避免新增依赖,先复用 UUID 工具。
func generateTaskID() string {
	return "t_" + randomHex(16)
}
