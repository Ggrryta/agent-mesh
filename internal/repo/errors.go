package repo

import (
	"errors"

	"github.com/go-sql-driver/mysql"
)

// isDuplicateKeyError 判断是否为 MySQL 唯一键冲突错误（error 1062）
func isDuplicateKeyError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
