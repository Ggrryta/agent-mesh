package config

import (
	"agent-gateway/pkg/logger"
	"fmt"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"
)

// NacosClient Nacos 客户端（配置中心 + 服务发现）
type NacosClient struct {
	configClient  config_client.IConfigClient
	namingClient  naming_client.INamingClient
	cfg           NacosConfig
}

// NewNacosClient 创建 Nacos 客户端
func NewNacosClient(cfg NacosConfig) (*NacosClient, error) {
	if !cfg.Enabled {
		logger.Info("nacos is disabled, skipping initialization")
		return nil, nil
	}

	// 创建服务器配置
	serverConfigs := []constant.ServerConfig{
		*constant.NewServerConfig(cfg.Host, cfg.Port),
	}

	// 创建客户端配置
	clientConfig := constant.ClientConfig{
		AppName:           "agent-gateway",
		NamespaceId:       cfg.Namespace,
		Username:          cfg.Username,
		Password:          cfg.Password,
		LogDir:            cfg.CacheDir + "/log",
		CacheDir:          cfg.CacheDir + "/cache",
		LogLevel:          cfg.LogLevel,
		NotLoadCacheAtStart: true,
	}

	clientParam := vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: serverConfigs,
	}

	// 创建配置客户端
	configClient, err := clients.NewConfigClient(clientParam)
	if err != nil {
		return nil, err
	}

	// 创建服务发现客户端
	namingClient, err := clients.NewNamingClient(clientParam)
	if err != nil {
		return nil, err
	}

	logger.Info("nacos client initialized",
		zap.String("host", cfg.Host),
		zap.Uint64("port", cfg.Port),
		zap.String("namespace", cfg.Namespace),
		zap.String("group", cfg.Group))

	return &NacosClient{
		configClient: configClient,
		namingClient: namingClient,
		cfg:          cfg,
	}, nil
}

// GetConfig 获取配置
func (c *NacosClient) GetConfig(dataID, group string) (string, error) {
	content, err := c.configClient.GetConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
	})
	if err != nil {
		return "", err
	}
	return content, nil
}

// ListenConfig 监听配置变更
func (c *NacosClient) ListenConfig(dataID, group string, onChange func(namespace, group, dataId, data string)) error {
	err := c.configClient.ListenConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
		OnChange: onChange,
	})
	if err != nil {
		return err
	}

	logger.Info("nacos config listener started",
		zap.String("data_id", dataID),
		zap.String("group", group))

	return nil
}

// PublishConfig 发布配置（用于 Admin API）
func (c *NacosClient) PublishConfig(dataID, group, content string) (bool, error) {
	ok, err := c.configClient.PublishConfig(vo.ConfigParam{
		DataId:  dataID,
		Group:   group,
		Content: content,
	})
	if err != nil {
		return false, err
	}

	logger.Info("nacos config published",
		zap.String("data_id", dataID),
		zap.String("group", group))

	return ok, nil
}

// Close 关闭客户端
func (c *NacosClient) Close() {
	// Nacos SDK 无需显式关闭
	logger.Info("nacos client closed")
}

// NamingClient 返回底层 naming client（供 discovery 包使用）
func (c *NacosClient) NamingClient() naming_client.INamingClient {
	return c.namingClient
}

// ---- 服务发现相关方法 ----

// SelectOneInstance 从 Nacos 选择一个健康的实例（支持负载均衡）
// serviceName: 服务名称
// groupName: 分组名称（默认 DEFAULT_GROUP）
// 返回: ip:port 格式的地址
func (c *NacosClient) SelectOneInstance(serviceName, groupName string) (string, error) {
	if c.namingClient == nil {
		return "", fmt.Errorf("nacos naming client not initialized")
	}
	if groupName == "" {
		groupName = "DEFAULT_GROUP"
	}

	// 使用 Nacos 内置的负载均衡策略（默认轮询）
	instance, err := c.namingClient.SelectOneHealthyInstance(vo.SelectOneHealthInstanceParam{
		ServiceName: serviceName,
		GroupName:   groupName,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:%d", instance.Ip, instance.Port), nil
}

// GetAllInstances 获取服务的所有健康实例
func (c *NacosClient) GetAllInstances(serviceName, groupName string) ([]model.Instance, error) {
	if c.namingClient == nil {
		return nil, fmt.Errorf("nacos naming client not initialized")
	}
	if groupName == "" {
		groupName = "DEFAULT_GROUP"
	}

	instances, err := c.namingClient.SelectAllInstances(vo.SelectAllInstancesParam{
		ServiceName: serviceName,
		GroupName:   groupName,
	})
	if err != nil {
		return nil, err
	}

	return instances, nil
}

// SubscribeService 订阅服务变更事件（实例上下线自动通知）
func (c *NacosClient) SubscribeService(serviceName, groupName string, onChange func(instances []model.Instance)) error {
	if c.namingClient == nil {
		return fmt.Errorf("nacos naming client not initialized")
	}
	if groupName == "" {
		groupName = "DEFAULT_GROUP"
	}

	err := c.namingClient.Subscribe(&vo.SubscribeParam{
		ServiceName: serviceName,
		GroupName:   groupName,
		SubscribeCallback: func(instances []model.Instance, err error) {
			if err != nil {
				logger.Error("nacos service subscription error",
					zap.String("service_name", serviceName),
					zap.Error(err))
				return
			}

			logger.Info("nacos service instances changed",
				zap.String("service_name", serviceName),
				zap.Int("instance_count", len(instances)))

			onChange(instances)
		},
	})
	if err != nil {
		return err
	}

	logger.Info("nacos service subscription started",
		zap.String("service_name", serviceName),
		zap.String("group", groupName))

	return nil
}
