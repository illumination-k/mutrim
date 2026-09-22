// Package concurrency exercises the concurrency operator.
package concurrency

import (
	"context"
	"sync"
	"sync/atomic"
)

// Counter is a mutex-guarded counter.
type Counter struct {
	mu sync.Mutex
	n  int
}

// Inc increments the counter under its lock.
func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

// Sum adds xs on one goroutine per element.
func Sum(xs []int) int64 {
	var total int64
	var wg sync.WaitGroup
	for _, x := range xs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt64(&total, int64(x))
		}()
	}
	wg.Wait()
	return total
}

// Produce sends xs on a channel buffered for all of them, then closes it.
func Produce(xs []int) <-chan int {
	ch := make(chan int, len(xs))
	for _, x := range xs {
		ch <- x
	}
	close(ch)
	return ch
}

// Buffered makes a channel buffered for n values.
func Buffered(n int) chan int {
	return make(chan int, n)
}

// First returns the first value of ch, or false once ctx is done.
func First(ctx context.Context, ch <-chan int) (int, bool) {
	select {
	case v, ok := <-ch:
		return v, ok
	case <-ctx.Done():
		return 0, false
	}
}

// Handoff passes v through an unbuffered channel to a goroutine.
func Handoff(v int) int {
	ch := make(chan int)
	done := make(chan int, 1)
	go func() { done <- <-ch }()
	ch <- v
	return <-done
}

var (
	once  sync.Once
	inits int
)

// Init runs the initialization once.
func Init() int {
	once.Do(func() { inits++ })
	return inits
}

// Flag stores 1 in the flag.
func Flag(f *int32) {
	atomic.StoreInt32(f, 1)
	var g int32
	atomic.StoreInt32(&g, 1)
}
