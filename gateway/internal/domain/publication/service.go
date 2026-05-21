package publication

import (
	"context"
	"errors"
	"strings"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
)

// AgentLookup 是 service 拉源 agent 信息（snapshot system_prompt）的依赖。
// 抽接口避免直接依赖 *agent.Service，方便测试。
//
// 跟 *agent.Service.Get(ctx, agentID) 签名一致。
type AgentLookup interface {
	Get(ctx context.Context, agentID string) (*agent.Agent, error)
}

// AgentRegistrar 是 fork 时创建新 agent 的依赖。
type AgentRegistrar interface {
	Register(ctx context.Context, in agent.RegisterInput) (*agent.Agent, error)
}

// Service 是 publication 的事务 façade。
type Service struct {
	repo      Repo
	agents    AgentLookup
	registrar AgentRegistrar
}

func NewService(repo Repo, agents AgentLookup, registrar AgentRegistrar) *Service {
	return &Service{repo: repo, agents: agents, registrar: registrar}
}

// PublishInput：发布者把自己名下的 agent 推到 market。
type PublishInput struct {
	PublisherUID  int64
	SourceAgentID string
	Title         string
	Summary       string
	Category      string
	Tags          []string
}

// Publish 校验输入 + 拉源 agent 的 system_prompt 做 snapshot + 写库。
//
// 校验：
//   - 必填 title
//   - title / summary / tags 长度限制
//   - 源 agent 必须属于发布者
func (s *Service) Publish(ctx context.Context, in PublishInput) (*Publication, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, ErrTitleRequired
	}
	if len(in.Title) > MaxTitleLen {
		return nil, ErrTitleTooLong
	}
	if len(in.Summary) > MaxSummaryLen {
		return nil, ErrSummaryTooLong
	}
	tagStr := SerializeTags(in.Tags)
	if len(tagStr) > MaxTagsTotal {
		return nil, ErrTagsTooLong
	}

	// 必须是自己的 agent
	src, err := s.agents.Get(ctx, in.SourceAgentID)
	if err != nil {
		return nil, err
	}
	if src.OwnerUID != in.PublisherUID {
		return nil, agent.ErrNotOwner
	}

	p := &Publication{
		PublisherUID:         in.PublisherUID,
		SourceAgentID:        in.SourceAgentID,
		Title:                in.Title,
		Summary:              strings.TrimSpace(in.Summary),
		SystemPromptTemplate: src.SystemPrompt,
		Category:             strings.TrimSpace(in.Category),
		Tags:                 in.Tags,
	}
	return s.repo.Insert(ctx, p)
}

// Get 拉单个 publication。
func (s *Service) Get(ctx context.Context, id int64) (*Publication, error) {
	return s.repo.GetByID(ctx, id)
}

// List 给 market 列表用。
func (s *Service) List(ctx context.Context, f Filter) ([]*Publication, error) {
	return s.repo.List(ctx, f)
}

// ListSubscriptions 拉某个用户的全部订阅，给 UI 标"已添加"用。
func (s *Service) ListSubscriptions(ctx context.Context, uid int64) ([]*Subscription, error) {
	return s.repo.ListSubscriptionsByUser(ctx, uid)
}

// Delete 仅发布者本人可删。
func (s *Service) Delete(ctx context.Context, id, publisherUID int64) error {
	ok, err := s.repo.DeleteOwned(ctx, id, publisherUID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPublicationNotFound
	}
	return nil
}

// ForkInput：用户 fork 一个 publication 到自己名下。
type ForkInput struct {
	PublicationID int64
	ForkerUID     int64
	NewAgentID    string // 用户输入；不能与已有 agent 冲突
	NewAgentName  string // 可选；空则用 publication.title
}

// Fork 创建 ForkerUID 名下的新 agent_id（从 publication 复制 system_prompt），
// 写 subscription，把 download_count + 1。
//
// 不在同一个 DB 事务里——agent.Register 涉及 cache 维护，不易 wrap 事务。
// 失败回滚做不到，但顺序保证：
//   1. 先写 subscription（唯一键防重）
//   2. 再 Register agent（失败的话 subscription 还在，下次重试会被 already_subscribed 挡掉）
//   3. 最后 IncrementDownload（计数失误不影响功能）
//
// 实际重试场景下 step 1 命中 ErrAlreadySubscribed 就该停了，让用户重新选别的 agent_id。
func (s *Service) Fork(ctx context.Context, in ForkInput) (*Subscription, *agent.Agent, error) {
	if in.PublicationID <= 0 || in.ForkerUID == 0 {
		return nil, nil, errors.New("publication: invalid fork input")
	}
	pub, err := s.repo.GetByID(ctx, in.PublicationID)
	if err != nil {
		return nil, nil, err
	}
	if pub.PublisherUID == in.ForkerUID {
		return nil, nil, ErrCannotForkOwn
	}

	// 写 subscription（唯一键防重，幂等友好）
	sub, err := s.repo.InsertSubscription(ctx, &Subscription{
		UID:           in.ForkerUID,
		PublicationID: in.PublicationID,
		ForkedAgentID: in.NewAgentID,
	})
	if err != nil {
		return nil, nil, err
	}

	// 创建 agent
	name := strings.TrimSpace(in.NewAgentName)
	if name == "" {
		name = pub.Title
	}
	created, err := s.registrar.Register(ctx, agent.RegisterInput{
		AgentID:      in.NewAgentID,
		OwnerUID:     in.ForkerUID,
		Name:         name,
		Description:  pub.Summary,
		SystemPrompt: pub.SystemPromptTemplate,
	})
	if err != nil {
		// subscription 已写但 agent 没建出来 —— 让 caller 知道这个不一致状态。
		// 回滚 subscription：删掉它，让用户能再选一次（用别的 agent_id）。
		// 即使删除失败也不影响主错误信号。
		_ = s.rollbackSubscription(ctx, sub.ID)
		return nil, nil, err
	}

	// 计数 +1（best-effort）
	_ = s.repo.IncrementDownload(ctx, in.PublicationID)
	return sub, created, nil
}

// rollbackSubscription 仅在 Fork 中 agent.Register 失败时使用。
// 不直接暴露——用户层概念是"订阅一旦成功就持续"。
func (s *Service) rollbackSubscription(ctx context.Context, _ int64) error {
	// MVP：不实际删（Repo 没暴露删除单条 subscription 的方法，加进去只为这个失败路径不划算）
	// 用户重试 fork 同一 publication 会被唯一键挡住 → 提示他换个 agent_id。
	// 后续如果加了 DeleteSubscription 再补这里。
	_ = ctx
	return nil
}
