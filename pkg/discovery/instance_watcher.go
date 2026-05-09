// Package discovery 提供基于 Nacos 的实例注册与感知
// 用于动态计算本地限流/并发配额（本地配额 = 全局配额 / 实例数）
package discovery

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"agent-gateway/pkg/logger"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"go.uber.org/zap"
)

// InstanceWatcher 基于 Nacos 服务发现的实例感知器
//
// 启动时向 Nacos 注册本实例，订阅同名服务的实例变更事件。
// 实例上下线时 Nacos 主动推送，回调更新 localRatio。
type InstanceWatcher struct {
	client      naming_client.INamingClient
	serviceName string
	ip          string
	port        uint64
	group       string

	mu            sync.RWMutex
	instanceCount int64
	callbacks     []func(count int)
}

// New 创建 InstanceWatcher
// serviceName: 在 Nacos 中注册的服务名（如 "agent-gateway"）
// ip/port: 本实例地址，用于注册
// group: Nacos 分组，空则使用 DEFAULT_GROUP
func New(client naming_client.INamingClient, serviceName, ip string, port uint64, group string) *InstanceWatcher {
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	return &InstanceWatcher{
		client:      client,
		serviceName: serviceName,
		ip:          ip,
		port:        port,
		group:       group,
	}
}

// OnChange 注册实例数变化回调
func (w *InstanceWatcher) OnChange(cb func(count int)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// Count 返回当前感知到的实例数（至少为 1）
func (w *InstanceWatcher) Count() int {
	n := int(atomic.LoadInt64(&w.instanceCount))
	if n < 1 {
		return 1
	}
	return n
}

// Start 注册本实例并订阅服务变更
func (w *InstanceWatcher) Start(_ context.Context) error {
	// 注册本实例到 Nacos
	ok, err := w.client.RegisterInstance(vo.RegisterInstanceParam{
		Ip:          w.ip,
		Port:        w.port,
		ServiceName: w.serviceName,
		GroupName:   w.group,
		Healthy:     true,
		Enable:      true,
		Weight:      1,
		Ephemeral:   true, // 临时实例：进程退出后 Nacos 自动摘除
	})
	if err != nil {
		return fmt.Errorf("nacos register instance failed: %w", err)
	}
	if !ok {
		return fmt.Errorf("nacos register instance returned false")
	}

	// 查一次当前实例数作为初始值
	instances, err := w.client.SelectAllInstances(vo.SelectAllInstancesParam{
		ServiceName: w.serviceName,
		GroupName:   w.group,
	})
	if err != nil {
		logger.Warn("nacos select all instances failed, defaulting to 1", zap.Error(err))
		atomic.StoreInt64(&w.instanceCount, 1)
	} else {
		w.applyInstances(instances)
	}

	// 订阅服务变更，实例上下线时 Nacos 主动推送
	return w.client.Subscribe(&vo.SubscribeParam{
		ServiceName: w.serviceName,
		GroupName:   w.group,
		SubscribeCallback: func(instances []model.Instance, err error) {
			if err != nil {
				logger.Error("nacos subscription error", zap.Error(err))
				return
			}
			w.applyInstances(instances)
		},
	})
}

// Stop 注销本实例并取消订阅
func (w *InstanceWatcher) Stop() {
	_ = w.client.Unsubscribe(&vo.SubscribeParam{
		ServiceName: w.serviceName,
		GroupName:   w.group,
	})
	_, _ = w.client.DeregisterInstance(vo.DeregisterInstanceParam{
		Ip:          w.ip,
		Port:        w.port,
		ServiceName: w.serviceName,
		GroupName:   w.group,
		Ephemeral:   true,
	})
}

// applyInstances 更新实例数并触发回调
func (w *InstanceWatcher) applyInstances(instances []model.Instance) {
	count := int64(len(instances))
	if count < 1 {
		count = 1
	}
	old := atomic.SwapInt64(&w.instanceCount, count)
	if old == count {
		return
	}
	logger.Info("instance count changed",
		zap.Int64("old", old),
		zap.Int64("new", count),
	)
	w.mu.RLock()
	cbs := w.callbacks
	w.mu.RUnlock()
	for _, cb := range cbs {
		cb(int(count))
	}
}
