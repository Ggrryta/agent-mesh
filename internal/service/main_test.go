package service

import (
	"os"
	"testing"

	"agent-gateway/pkg/logger"
)

func TestMain(m *testing.M) {
	_ = logger.Init("debug", "console")
	os.Exit(m.Run())
}
