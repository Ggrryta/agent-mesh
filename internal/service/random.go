package service

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex 生成 n 字节的随机 hex 字符串(长度为 2n)
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
