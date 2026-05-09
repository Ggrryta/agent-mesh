package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/logger"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	nacosModel "github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"
)

// AgentRegistryNotifier broadcasts agent state changes across gateway instances.
type AgentRegistryNotifier interface {
	RegisterAgent(ctx context.Context, agent *model.Agent) error
	DeregisterAgent(ctx context.Context, agentID, agentURL string) error
	Subscribe(onChange func(agentIDs []string)) error
	Stop()
}

// --- Nacos implementation ---

type nacosAgentRegistryNotifier struct {
	client      naming_client.INamingClient
	serviceName string
	group       string
}

func NewNacosAgentRegistryNotifier(client naming_client.INamingClient, serviceName, group string) AgentRegistryNotifier {
	if serviceName == "" {
		serviceName = "agent-registry"
	}
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	return &nacosAgentRegistryNotifier{
		client:      client,
		serviceName: serviceName,
		group:       group,
	}
}

func (n *nacosAgentRegistryNotifier) RegisterAgent(_ context.Context, agent *model.Agent) error {
	host, port, err := parseAgentURL(agent.URL)
	if err != nil {
		return fmt.Errorf("parse agent url: %w", err)
	}

	ok, err := n.client.RegisterInstance(vo.RegisterInstanceParam{
		Ip:          host,
		Port:        port,
		ServiceName: n.serviceName,
		GroupName:   n.group,
		Healthy:     true,
		Enable:      true,
		Weight:      1,
		Ephemeral:   false,
		Metadata: map[string]string{
			"agent_id":     agent.AgentID,
			"owner_app_id": agent.OwnerAppID,
			"version":      agent.Version,
		},
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("nacos register agent returned false")
	}
	return nil
}

func (n *nacosAgentRegistryNotifier) DeregisterAgent(_ context.Context, agentID, agentURL string) error {
	host, port, err := parseAgentURL(agentURL)
	if err != nil {
		return fmt.Errorf("parse agent url: %w", err)
	}

	_, err = n.client.DeregisterInstance(vo.DeregisterInstanceParam{
		Ip:          host,
		Port:        port,
		ServiceName: n.serviceName,
		GroupName:   n.group,
		Ephemeral:   false,
	})
	return err
}

func (n *nacosAgentRegistryNotifier) Subscribe(onChange func(agentIDs []string)) error {
	return n.client.Subscribe(&vo.SubscribeParam{
		ServiceName: n.serviceName,
		GroupName:   n.group,
		SubscribeCallback: func(instances []nacosModel.Instance, err error) {
			if err != nil {
				logger.Error("nacos agent registry subscription error", zap.Error(err))
				return
			}
			ids := make([]string, 0, len(instances))
			for _, inst := range instances {
				if id := inst.Metadata["agent_id"]; id != "" {
					ids = append(ids, id)
				}
			}
			onChange(ids)
		},
	})
}

func (n *nacosAgentRegistryNotifier) Stop() {
	_ = n.client.Unsubscribe(&vo.SubscribeParam{
		ServiceName: n.serviceName,
		GroupName:   n.group,
	})
}

// --- Noop implementation (Nacos disabled) ---

type noopAgentRegistryNotifier struct{}

func NewNoopAgentRegistryNotifier() AgentRegistryNotifier {
	return &noopAgentRegistryNotifier{}
}

func (n *noopAgentRegistryNotifier) RegisterAgent(context.Context, *model.Agent) error { return nil }
func (n *noopAgentRegistryNotifier) DeregisterAgent(context.Context, string, string) error {
	return nil
}
func (n *noopAgentRegistryNotifier) Subscribe(func([]string)) error { return nil }
func (n *noopAgentRegistryNotifier) Stop()                          {}

// --- Helpers ---

func parseAgentURL(rawURL string) (host string, port uint64, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, err
	}
	host = u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("empty host in url: %s", rawURL)
	}
	portStr := u.Port()
	if portStr != "" {
		p, err := strconv.ParseUint(portStr, 10, 64)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port in url: %s", rawURL)
		}
		port = p
	} else {
		switch u.Scheme {
		case "https":
			port = 443
		default:
			port = 80
		}
	}
	return host, port, nil
}
