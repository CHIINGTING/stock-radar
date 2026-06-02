package market

import (
	"log"
	"sync"
	"time"
)

const (
	QuoteTTL    = 60 * time.Second
	maxWorkers  = 3
	workerDelay = 500 * time.Millisecond
)

// Cache holds realtime quotes for all watched stocks.
// Data and LastUpdate are exported; all mutations go through the mutex.
// Do not copy Cache — always use a pointer.
type Cache struct {
	mu         sync.RWMutex
	Data       map[string]*Quote
	LastUpdate time.Time
	refreshing bool
}

func NewCache() *Cache {
	return &Cache{Data: make(map[string]*Quote)}
}

// Get returns a copy of the cached quote for code, or (nil, false) if not found.
func (c *Cache) Get(code string) (*Quote, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	q, ok := c.Data[code]
	if !ok {
		return nil, false
	}
	cp := *q
	return &cp, true
}

// Snapshot returns a full copy of all cached quotes and the last update time.
func (c *Cache) Snapshot() (map[string]*Quote, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]*Quote, len(c.Data))
	for k, v := range c.Data {
		cp := *v
		out[k] = &cp
	}
	return out, c.LastUpdate
}

// IsEmpty returns true when no data has been loaded yet.
func (c *Cache) IsEmpty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Data) == 0
}

// IsStale returns true when the last refresh is older than QuoteTTL.
func (c *Cache) IsStale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.LastUpdate) > QuoteTTL
}

// Refresh fetches all codes using a worker pool (≤ maxWorkers concurrent requests,
// workerDelay between each launch) and writes results into the cache.
//
// If a refresh is already running, this call returns immediately without blocking.
func (c *Cache) Refresh(codes []string, provider RealtimeProvider) {
	c.mu.Lock()
	if c.refreshing {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.refreshing = false
		c.mu.Unlock()
	}()

	if len(codes) == 0 {
		return
	}

	type item struct {
		code  string
		quote *Quote
	}

	collected := make(chan item, len(codes))
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for i, code := range codes {
		if i > 0 {
			// Rate-limit request starts: wait before launching the next goroutine.
			time.Sleep(workerDelay)
		}
		sem <- struct{}{} // acquire worker slot (blocks when maxWorkers are in-flight)
		wg.Add(1)
		go func(code string) {
			defer func() {
				<-sem // release worker slot
				wg.Done()
			}()
			q, err := provider.GetQuote(code)
			if err != nil {
				log.Printf("cache: refresh code=%s err=%v", code, err)
				collected <- item{code, nil}
				return
			}
			collected <- item{code, q}
		}(code)
	}

	wg.Wait()
	close(collected)

	c.mu.Lock()
	for it := range collected {
		if it.quote != nil {
			c.Data[it.code] = it.quote
		}
	}
	c.LastUpdate = time.Now()
	c.mu.Unlock()
}

// RefreshAsync starts a background Refresh and returns immediately.
// Ignored if a refresh is already in progress.
func (c *Cache) RefreshAsync(codes []string, provider RealtimeProvider) {
	go c.Refresh(codes, provider)
}
