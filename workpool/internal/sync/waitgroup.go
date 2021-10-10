package sync

import (
	"sync"
	"sync/atomic"
)

type ExtWaitGroup struct {
	sync.WaitGroup
	waitCount uint64
}

// Add 并返回新值
func (w *ExtWaitGroup) Add(n int) uint64 {
	w.WaitGroup.Add(n)
	return atomic.AddUint64(&w.waitCount, uint64(n))
}

// CompareAndAdd 判断值为预期的 old 值再加一
func (w *ExtWaitGroup) CompareAndAdd(old, delta uint64) bool {
	ok := atomic.CompareAndSwapUint64(&w.waitCount, old, old+delta)
	if !ok {
		return false
	}
	w.WaitGroup.Add(int(delta))
	return true
}
func (w *ExtWaitGroup) Done() {
	w.WaitGroup.Done()
	atomic.AddUint64(&w.waitCount, ^uint64(0))
}

func (w *ExtWaitGroup) GetWaitCount() uint64 {
	return w.waitCount
}
