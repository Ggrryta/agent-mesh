package grpc

// IdentityClient 是 Messaging Svc 用来调 Identity Svc 的 gRPC 客户端。
// 实现 task.AgentLookup 和 task.FriendshipChecker 接口，让 task.Service
// 无需知道底层是本地调用还是远程 gRPC。

import (
	"context"
	"time"

	pb "github.com/Ggrryta/agent-mesh/gateway/internal/grpc/identitypb"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// IdentityClient 封装 gRPC 连接，实现 task 域需要的接口。
type IdentityClient struct {
	client pb.IdentityServiceClient
	conn   *grpclib.ClientConn
}

// NewIdentityClient 连接 Identity Svc 的 gRPC 端点。
func NewIdentityClient(addr string) (*IdentityClient, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpclib.DialContext(ctx, addr,
		grpclib.WithTransportCredentials(insecure.NewCredentials()),
		grpclib.WithBlock(),
		grpclib.WithDefaultCallOptions(grpclib.WaitForReady(true)),
		grpclib.WithUnaryInterceptor(timeoutInterceptor(3*time.Second)), // 每次调用 3s 超时
	)
	if err != nil {
		return nil, err
	}
	return &IdentityClient{
		client: pb.NewIdentityServiceClient(conn),
		conn:   conn,
	}, nil
}

// timeoutInterceptor 给每次 gRPC 调用加默认超时（防止 Identity 挂了拖垮 Messaging）。
func timeoutInterceptor(timeout time.Duration) grpclib.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpclib.ClientConn, invoker grpclib.UnaryInvoker, opts ...grpclib.CallOption) error {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// Close 关闭 gRPC 连接。
func (c *IdentityClient) Close() error {
	return c.conn.Close()
}

// ── 实现 task.AgentLookup 接口 ──

func (c *IdentityClient) Lookup(ctx context.Context, agentID string) (ownerUID int64, kind string, found bool) {
	resp, err := c.client.LookupAgentOwner(ctx, &pb.LookupAgentOwnerRequest{AgentId: agentID})
	if err != nil || resp == nil {
		return 0, "", false
	}
	return resp.OwnerUid, resp.Kind, resp.Found
}

// ── 实现 task.FriendshipChecker 接口 ──

func (c *IdentityClient) AreFriends(ctx context.Context, a, b string) (bool, error) {
	resp, err := c.client.CanCommunicate(ctx, &pb.CanCommunicateRequest{AgentA: a, AgentB: b})
	if err != nil {
		return false, err
	}
	return resp.Allowed, nil
}
