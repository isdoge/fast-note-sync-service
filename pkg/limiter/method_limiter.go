package limiter

import (
	"container/list"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/juju/ratelimit"
)

type MethodLimiter struct {
	*Limiter
	mu      sync.Mutex
	rules   map[string]BucketRule
	clients map[string]*list.Element
	order   *list.List
}

type clientBucket struct {
	key      string
	bucket   *ratelimit.Bucket
	lastUsed time.Time
}

const maxClientBuckets = 4096
const clientBucketIdle = 10 * time.Minute

func NewMethodLimiter() Face {
	l := &Limiter{limiterBuckets: make(map[string]*ratelimit.Bucket)}
	return &MethodLimiter{
		Limiter: l,
		rules:   make(map[string]BucketRule),
		clients: make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (l *MethodLimiter) Key(c *gin.Context) string {
	// Match the route Gin actually dispatched, including URL-encoded aliases.
	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}
	l.mu.Lock()
	perClient := l.rules[path].PerClient
	l.mu.Unlock()
	if perClient {
		return path + "\x00" + clientKey(c.ClientIP())
	}
	return path
}

// clientKey 返回按客户端限流的 key：IPv4 按单个地址；IPv6 按 /64 前缀聚合，
// 防止攻击者在同一 /64（通常是一个用户/站点的分配单位）内轮换地址绕过单 IP 限流。
func clientKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func (l *MethodLimiter) GetBucket(key string) (*ratelimit.Bucket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path, _, perClient := strings.Cut(key, "\x00")
	if perClient {
		rule, ok := l.rules[path]
		if !ok || !rule.PerClient {
			return nil, false
		}
		now := time.Now()
		for oldest := l.order.Back(); oldest != nil && now.Sub(oldest.Value.(*clientBucket).lastUsed) > clientBucketIdle; oldest = l.order.Back() {
			delete(l.clients, oldest.Value.(*clientBucket).key)
			l.order.Remove(oldest)
		}
		if element, ok := l.clients[key]; ok {
			entry := element.Value.(*clientBucket)
			entry.lastUsed = now
			l.order.MoveToFront(element)
			return entry.bucket, true
		}
		if l.order.Len() >= maxClientBuckets {
			oldest := l.order.Back()
			delete(l.clients, oldest.Value.(*clientBucket).key)
			l.order.Remove(oldest)
		}
		entry := &clientBucket{key: key, bucket: ratelimit.NewBucketWithQuantum(rule.FillInterval, rule.Capacity, rule.Quantum), lastUsed: now}
		l.clients[key] = l.order.PushFront(entry)
		return entry.bucket, true
	}
	bucket, ok := l.limiterBuckets[key]
	return bucket, ok
}

func (l *MethodLimiter) AddBuckets(rules ...BucketRule) Face {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rule := range rules {
		if _, ok := l.limiterBuckets[rule.Key]; !ok {
			l.rules[rule.Key] = rule
			bucket := ratelimit.NewBucketWithQuantum(
				rule.FillInterval,
				rule.Capacity,
				rule.Quantum,
			)
			l.limiterBuckets[rule.Key] = bucket
		}
	}

	return l
}
