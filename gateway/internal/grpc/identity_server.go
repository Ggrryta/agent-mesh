// Package grpc 提供 Identity Service 的 gRPC server 实现。
// Identity Svc 启动时注册到 gRPC server，供 Messaging Svc 跨服务调用。
package grpc

import (
	"context"

	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/agent"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/friendship"
	"github.com/Ggrryta/agent-mesh/gateway/internal/domain/group"
	pb "github.com/Ggrryta/agent-mesh/gateway/internal/grpc/identitypb"

	grpclib "google.golang.org/grpc"
)

// IdentityServer 实现 pb.IdentityServiceServer。
type IdentityServer struct {
	pb.UnimplementedIdentityServiceServer
	agents  *agent.LookupAdapter
	agentSvc *agent.Service
	friends *friendship.Service
	groups  *group.Service
}

func NewIdentityServer(agentSvc *agent.Service, friends *friendship.Service, groups *group.Service) *IdentityServer {
	return &IdentityServer{
		agents:   agent.NewLookupAdapter(agentSvc),
		agentSvc: agentSvc,
		friends:  friends,
		groups:   groups,
	}
}

func (s *IdentityServer) Register(srv *grpclib.Server) {
	pb.RegisterIdentityServiceServer(srv, s)
}

func (s *IdentityServer) CanCommunicate(ctx context.Context, req *pb.CanCommunicateRequest) (*pb.CanCommunicateResponse, error) {
	// 检查好友关系
	ok, err := s.friends.AreFriends(ctx, req.AgentA, req.AgentB)
	if err != nil {
		return nil, err
	}
	if ok {
		return &pb.CanCommunicateResponse{Allowed: true}, nil
	}
	// 检查同群
	if s.groups != nil {
		same, err := s.groups.SameGroup(ctx, req.AgentA, req.AgentB)
		if err == nil && same {
			return &pb.CanCommunicateResponse{Allowed: true}, nil
		}
	}
	return &pb.CanCommunicateResponse{Allowed: false}, nil
}

func (s *IdentityServer) LookupAgentOwner(ctx context.Context, req *pb.LookupAgentOwnerRequest) (*pb.LookupAgentOwnerResponse, error) {
	ownerUID, kind, found := s.agents.Lookup(ctx, req.AgentId)
	return &pb.LookupAgentOwnerResponse{
		OwnerUid: ownerUID,
		Kind:     kind,
		Found:    found,
	}, nil
}

func (s *IdentityServer) GetAgent(ctx context.Context, req *pb.GetAgentRequest) (*pb.AgentInfo, error) {
	a, err := s.agentSvc.Get(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return &pb.AgentInfo{
		AgentId:  a.AgentID,
		Name:     a.Name,
		Status:   string(a.Status),
		OwnerUid: a.OwnerUID,
		Kind:     string(a.Kind),
	}, nil
}
