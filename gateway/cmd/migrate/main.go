// migrate 是在 goose 之上薄薄一层 CLI，把 embed 进二进制的 SQL migration
// 跑到 MYSQL_DSN 指定的库上。
//
// 用法：
//
//	migrate up            应用所有未执行的 migration（默认）。
//	migrate up-by-one     只跑下一个 migration。
//	migrate down          回滚最近一次 migration。
//	migrate status        查看已应用的状态。
//	migrate version       打印当前版本号。
//	migrate reset         全部回滚（仅限 DEV）。
//	migrate create NAME   新建一个 migration 文件（会改动仓库）。
//
// DSN 从 MYSQL_DSN 读，不用传 flag。
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"github.com/Ggrryta/agent-mesh/gateway/migrations"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
)

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		args = []string{"up"}
	}

	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate: MYSQL_DSN env var is required")
		os.Exit(2)
	}

	// migrations.FS 的文件直接在根目录下，正好是 goose 想要的形状。
	var sub fs.FS = migrations.FS
	goose.SetBaseFS(sub)

	if err := goose.SetDialect("mysql"); err != nil {
		die("set dialect", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		die("open", err)
	}
	defer db.Close()

	cmd := args[0]
	rest := args[1:]
	if err := goose.Run(cmd, db, ".", rest...); err != nil {
		die("run "+cmd, err)
	}
}

func die(stage string, err error) {
	fmt.Fprintf(os.Stderr, "migrate: %s failed: %v\n", stage, err)
	os.Exit(1)
}
