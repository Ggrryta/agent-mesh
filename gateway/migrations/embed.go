// Package migrations 把 SQL migration 文件 embed 进二进制，让 migrate CLI
// 和集成测试都能直接访问，不用关心相对路径。
package migrations

import "embed"

// FS 持有所有和本 Go 文件放在同目录的 000x_*.sql。
//
//go:embed *.sql
var FS embed.FS
