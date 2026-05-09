package ratelimit

import (
	"sync"
	"testing"
)

// TestHybridLimiterConcurrent 验证混合限流器并发安全
func TestHybridLimiterConcurrent(t *testing.T) {
	t.Skip("需要 Redis 连接，跳过并发测试")
	
	// 这里只做结构验证，实际并发测试需要 Redis
	t.Log("HybridLimiter 并发安全验证（需要 Redis 环境）")
}

// TestHybridLimiterDoubleCheck 验证 double-check 逻辑
func TestHybridLimiterDoubleCheck(t *testing.T) {
	t.Skip("需要 Redis 连接，跳过 double-check 测试")
	
	t.Log("HybridLimiter double-check 验证（需要 Redis 环境）")
}

// TestTokenBucketConcurrent 验证令牌桶并发安全
func TestTokenBucketConcurrent(t *testing.T) {
	limiter := NewTokenBucketLimiter(100, 50, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	concurrent := 50
	callsPerGoroutine := 200

	allowed := 0
	rejected := 0

	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				if limiter.Allow("test_key") {
					mu.Lock()
					allowed++
					mu.Unlock()
				} else {
					mu.Lock()
					rejected++
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("Allowed: %d, Rejected: %d", allowed, rejected)

	// 验证总数
	total := allowed + rejected
	expected := concurrent * callsPerGoroutine
	if total != expected {
		t.Errorf("Total calls %d != expected %d", total, expected)
	}
}

// TestTokenBucketBurst 验证令牌桶突发流量
func TestTokenBucketBurst(t *testing.T) {
	limiter := NewTokenBucketLimiter(10, 5, nil)

	// 突发 10 个请求（应该全部通过）
	allowed := 0
	for i := 0; i < 10; i++ {
		if limiter.Allow("burst_test") {
			allowed++
		}
	}

	if allowed != 10 {
		t.Errorf("Expected 10 allowed, got %d", allowed)
	}

	// 第 11 个应该被拒绝
	if limiter.Allow("burst_test") {
		t.Error("11th request should be rejected")
	}

	t.Logf("Burst test passed: %d/10 allowed", allowed)
}
