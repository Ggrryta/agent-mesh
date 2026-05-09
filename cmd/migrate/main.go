package main

import (
	"flag"
	"fmt"
	"os"

	"agent-gateway/config"
	"agent-gateway/internal/model"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var configPath = flag.String("config", "config/config.yaml", "配置文件路径")

func main() {
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Printf("load config failed: %v\n", err)
		os.Exit(1)
	}

	db, err := gorm.Open(mysql.Open(cfg.Database.DSN()), &gorm.Config{})
	if err != nil {
		fmt.Printf("connect mysql failed: %v\n", err)
		os.Exit(1)
	}

	if err := db.AutoMigrate(
		&model.Consumer{},
		&model.ReliableAsyncTask{},
		&model.OutboxEvent{},
		&model.APIKey{},
		&model.Agent{},
		&model.AgentSkill{},
		&model.AgentPermission{},
		&model.AgentApply{},
		&model.ConfigVersion{},
		&model.Friendship{},
		&model.TaskV2{},
		&model.TaskMember{},
		&model.TaskMessage{},
	); err != nil {
		fmt.Printf("migrate failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("migration done")
}
