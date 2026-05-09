// minigw —— 独立的精简 Gateway binary,供 M7 e2e 测试用
//
// 编译: go build -o /tmp/minigw ./test/e2e/cmd/minigw
// 启动: minigw [-seed-file <path>]
//   打印一行 JSON: {"addr":"127.0.0.1:<port>","pid":...} 到 stdout
// 退出: 收 SIGINT/SIGTERM
//
// -seed-file 可选,YAML 或 JSON,格式:
//   agents:
//     - {id: alice, name: Alice, owner_app_id: app1}
//     - {id: bob, name: Bob, owner_app_id: app2}
//   api_keys:
//     - {app_id: app1, key: agw_alice_key}
//     - {app_id: app2, key: agw_bob_key}
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agent-gateway/internal/model"
	"agent-gateway/pkg/logger"
	"agent-gateway/test/e2e/minigwlib"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/crypto/bcrypt"
)

type seedFile struct {
	Agents []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		OwnerAppID   string `json:"owner_app_id"`
		DeliveryMode int    `json:"delivery_mode"`
	} `json:"agents"`
	APIKeys []struct {
		AppID string `json:"app_id"`
		Key   string `json:"key"`
	} `json:"api_keys"`
}

func main() {
	seedPath := flag.String("seed-file", "", "JSON seed file path")
	logLevel := flag.String("log-level", "info", "log level")
	listenAddr := flag.String("listen", "127.0.0.1:0",
		"listen addr. 用 0.0.0.0:11556 让局域网可达")
	dbPath := flag.String("db", "", "SQLite 路径;空=临时文件,进程退出即丢")
	flag.Parse()

	// 初始化全局 logger 到 stderr(pkg/logger.Init 默认写 os.Stdout,会污染 ready JSON,
	// 且 stdout PIPE buffer 填满会阻塞整个 Gateway)
	if err := logger.Init(*logLevel, "json"); err != nil {
		fmt.Fprintln(os.Stderr, "logger init failed:", err)
		os.Exit(1)
	}
	// 重建 Logger 指向 stderr
	redirectLoggerToStderr(*logLevel)
	defer logger.Sync()

	inst, err := minigwlib.StartWithOptions(minigwlib.Options{
		SQLitePath: *dbPath,
		ListenAddr: *listenAddr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start failed:", err)
		os.Exit(1)
	}

	if *seedPath != "" {
		if err := applySeed(*seedPath, inst); err != nil {
			fmt.Fprintln(os.Stderr, "seed failed:", err)
			os.Exit(1)
		}
	}

	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"addr": inst.Addr,
		"pid":  os.Getpid(),
		"ts":   time.Now().Unix(),
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inst.Stop(ctx)
}

func applySeed(path string, inst *minigwlib.Instance) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s seedFile
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("parse seed: %w", err)
	}
	ctx := context.Background()

	for _, a := range s.Agents {
		mode := model.DeliveryMode(a.DeliveryMode)
		agent := &model.Agent{
			AgentID:      a.ID,
			Name:         a.Name,
			OwnerAppID:   a.OwnerAppID,
			Status:       model.AgentStatusActive,
			DeliveryMode: mode,
		}
		if err := inst.AgentRepo.Create(ctx, agent); err != nil {
			return fmt.Errorf("seed agent %s: %w", a.ID, err)
		}
	}
	for _, k := range s.APIKeys {
		hash, err := bcrypt.GenerateFromPassword([]byte(k.Key), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		prefix := k.Key[:8]
		if err := inst.APIKeyRepo.Upsert(ctx, k.AppID, string(hash), prefix); err != nil {
			return fmt.Errorf("seed api key %s: %w", k.AppID, err)
		}
	}
	return nil
}

// redirectLoggerToStderr 重建 logger.Logger 让它输出到 stderr
// pkg/logger.Init 默认写 os.Stdout,但本 binary 的 stdout 用于传递 ready JSON
// 而且 PIPE buffer 填满会阻塞后续写入,导致 Gateway 挂起
func redirectLoggerToStderr(level string) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		lvl = zapcore.InfoLevel
	}
	encoderConfig := zapcore.EncoderConfig{
		TimeKey: "time", LevelKey: "level", NameKey: "logger",
		MessageKey: "msg", StacktraceKey: "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	}
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(os.Stderr),
		zap.NewAtomicLevelAt(lvl),
	)
	logger.Logger = zap.New(core)
}
