package service

import (
	"context"
	"encoding/json"
	"errors"

	"agent-gateway/internal/model"
	"agent-gateway/internal/repo"
	"agent-gateway/pkg/logger"

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
	Sender    string            // 发送方 agent_id
	Target    string            // 目标 agent_id
	TaskID    string            // 为空 = 新建 task
	Title     string            // 新建 task 时用
	MessageID string            // 发送方生成的 UUID
	Parts     []A2AMessagePart  // A2A 消息内容
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
)

// AgentDispatcher 消息投递中枢
//
//	1. 检查 sender 存在且 active
//	2. 检查 target 存在且 active
//	3. 检查好友关系
//	4. 对 pull 模式 agent:检查在线(OnlineRegistry)
//	5. 创建/追加 task_messages
//	6. 通过 InboxHub 推送给 target
type AgentDispatcher struct {
	agentRepo       *repo.AgentRepo
	friendRepo      *repo.FriendshipRepo
	taskRepo        *repo.TaskV2Repo
	onlineReg       *OnlineRegistry
	hub             *InboxHub
	pushInvoker     *A2AInvoker // 保留 push 模式兼容(本期 pull 优先)
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

// SendMessage 统一发送入口
func (d *AgentDispatcher) SendMessage(ctx context.Context, in SendMessageInput) (*SendMessageResult, error) {
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
	return nil
}

// generateTaskID 简洁的 task_id 生成器。使用 UUID,但项目中多数地方直接用 google/uuid。
// 这里为了避免新增依赖,先复用 UUID 工具。
func generateTaskID() string {
	return "t_" + randomHex(16)
}
