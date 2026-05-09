package config

import (
	"agent-gateway/pkg/logger"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

// CanaryMode 灰度模式
type CanaryMode string

const (
	CanaryModeNone   CanaryMode = "none"   // 非灰度实例
	CanaryModeConfig CanaryMode = "config" // 配置灰度（独立 DataID）
	CanaryModeTraffic CanaryMode = "traffic" // 流量灰度（独立实例）
)

// CanaryConfig 灰度配置
type CanaryConfig struct {
	Enabled     bool       `json:"enabled"`      // 是否启用灰度
	Mode        CanaryMode `json:"mode"`         // 灰度模式
	CanaryID    string     `json:"canary_id"`    // 灰度标识（如 beta, v2）
	DataIDSuffix string    `json:"data_id_suffix"` // 配置 DataID 后缀（如 -beta）
	Weight      int        `json:"weight"`       // 流量权重 0-100
}

// GetCanaryConfig 从环境变量和配置获取灰度配置
// 优先级：环境变量 > 配置文件
func GetCanaryConfig(nacosCfg NacosConfig) *CanaryConfig {
	cfg := &CanaryConfig{
		Enabled:     false,
		Mode:        CanaryModeNone,
		CanaryID:    "",
		DataIDSuffix: "",
		Weight:      0,
	}

	// 从环境变量读取灰度配置
	canaryEnabled := strings.ToLower(os.Getenv("CANARY_ENABLED"))
	if canaryEnabled == "true" || canaryEnabled == "1" {
		cfg.Enabled = true

		// 灰度模式
		mode := os.Getenv("CANARY_MODE")
		switch strings.ToLower(mode) {
		case "config":
			cfg.Mode = CanaryModeConfig
		case "traffic":
			cfg.Mode = CanaryModeTraffic
		default:
			cfg.Mode = CanaryModeConfig // 默认配置灰度
		}

		// 灰度标识
		cfg.CanaryID = os.Getenv("CANARY_ID")
		if cfg.CanaryID == "" {
			cfg.CanaryID = "beta"
		}

		// 配置 DataID 后缀
		cfg.DataIDSuffix = os.Getenv("CANARY_DATA_ID_SUFFIX")
		if cfg.DataIDSuffix == "" {
			cfg.DataIDSuffix = "-" + cfg.CanaryID
		}

		// 流量权重（仅 traffic 模式）
		if cfg.Mode == CanaryModeTraffic {
			weight := os.Getenv("CANARY_WEIGHT")
			if weight != "" {
				fmt.Sscanf(weight, "%d", &cfg.Weight)
				if cfg.Weight < 0 || cfg.Weight > 100 {
					cfg.Weight = 10
				}
			} else {
				cfg.Weight = 10 // 默认 10% 流量
			}
		}

		logger.Info("canary mode enabled",
			zap.String("mode", string(cfg.Mode)),
			zap.String("canary_id", cfg.CanaryID),
			zap.String("data_id_suffix", cfg.DataIDSuffix),
			zap.Int("weight", cfg.Weight))
	}

	return cfg
}

// BuildCanaryDataID 构建灰度配置 DataID
// 正常: agent-gateway
// 灰度: agent-gateway-beta
func (c *CanaryConfig) BuildCanaryDataID(baseDataID string) string {
	if !c.Enabled || c.Mode != CanaryModeConfig {
		return baseDataID
	}
	return baseDataID + c.DataIDSuffix
}

// IsCanaryInstance 判断是否为灰度实例
func (c *CanaryConfig) IsCanaryInstance() bool {
	return c.Enabled
}

// GetCanaryMetadata 获取灰度实例元数据（用于服务注册）
func (c *CanaryConfig) GetCanaryMetadata() map[string]string {
	if !c.Enabled {
		return nil
	}

	metadata := map[string]string{
		"canary":    "true",
		"canary_id": c.CanaryID,
	}

	if c.Mode == CanaryModeTraffic {
		metadata["canary_weight"] = fmt.Sprintf("%d", c.Weight)
	}

	return metadata
}
