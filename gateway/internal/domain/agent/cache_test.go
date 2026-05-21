package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mkAgent(id string, status Status) *Agent {
	return &Agent{
		AgentID: id,
		Name:    id,
		Status:  status,
	}
}

func TestCache_BasicSetGetDelete(t *testing.T) {
	c := NewCache()
	if _, ok := c.Get("nope"); ok {
		t.Fatal("expected miss")
	}
	c.Set(mkAgent("alice", StatusActive))
	a, ok := c.Get("alice")
	if !ok || a.Name != "alice" {
		t.Fatalf("get: %v %+v", ok, a)
	}
	if c.Len() != 1 {
		t.Fatalf("len=%d", c.Len())
	}
	c.Delete("alice")
	if _, ok := c.Get("alice"); ok {
		t.Fatal("delete didn't take")
	}
}

func TestCache_GetActiveFiltersStatus(t *testing.T) {
	c := NewCache()
	c.Set(mkAgent("alice", StatusDraining))
	if _, ok := c.GetActive("alice"); ok {
		t.Fatal("draining should not be GetActive")
	}
	c.Set(mkAgent("bob", StatusActive))
	if _, ok := c.GetActive("bob"); !ok {
		t.Fatal("active should be GetActive")
	}
}

func TestCache_Reload(t *testing.T) {
	c := NewCache()
	c.Set(mkAgent("stale", StatusActive))
	c.Reload([]*Agent{mkAgent("fresh", StatusActive)})
	if _, ok := c.Get("stale"); ok {
		t.Fatal("Reload should replace, not merge")
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Fatal("Reload should contain fresh entry")
	}
}

func TestCache_EachEarlyStop(t *testing.T) {
	c := NewCache()
	for _, id := range []string{"a", "b", "c"} {
		c.Set(mkAgent(id, StatusActive))
	}
	seen := 0
	c.Each(func(a *Agent) bool {
		seen++
		return false // stop immediately
	})
	if seen != 1 {
		t.Fatalf("early-stop: seen=%d", seen)
	}
}

// TestCache_ConcurrentReadsDuringWrites 验证核心不变量：
// 读者不会阻塞，也不会看到半构建的 snapshot。
func TestCache_ConcurrentReadsDuringWrites(t *testing.T) {
	c := NewCache()
	for i := 0; i < 50; i++ {
		c.Set(&Agent{AgentID: idx(i), Name: idx(i), Status: StatusActive})
	}
	var stop atomic.Bool
	var wg sync.WaitGroup

	// 多个读者。
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_, _ = c.Get(idx(r))
				c.Each(func(a *Agent) bool {
					_ = a.AgentID // 摸一下字段
					return true
				})
			}
		}()
	}
	// 一个写者不停搅动。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.Set(&Agent{AgentID: idx(i % 50), Name: "churn", Status: StatusActive})
			if i%7 == 0 {
				c.Delete(idx(i % 50))
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	// 不做额外断言，"没死锁 / 没 race" 就算过；-race 给出真正的保证。
}

func idx(i int) string {
	return "agent-" + byteStr(i)
}

// byteStr 保持测试自包含，不引入 strconv。
func byteStr(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+(i%10))) + s
		i /= 10
	}
	return s
}
