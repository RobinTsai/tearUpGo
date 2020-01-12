package sync

import (
	"internal/race"
	"sync/atomic"
	"unsafe"
)

// 在 runtime 目录下有一个修改的 rwmutex.go 文件
// 如果你在此文件上做了修改，看是否需要修改另一个文件

// 一个 RWMutex 是一个读/写互斥锁（a reader/writer mutual exclusion lock）。
// 这个锁可以被任意数量的读者或一个写者拥有。
// RWMutex 的零值是一个未上锁的互斥量（mutex）。
//
// 一个 RWMutex 在使用后，一定不要被拷贝。
//
// 如果一个协程拥有了一个 RWMutex 读的权限，此时另一个协程可能调用了 Lock 函数，
// 这时没有协程能再获得这个读锁，直到这个最初的读锁被释放。
// 特别指出，这种特性防止了循环得读锁定（死锁）。
// 这是为了确保这个锁最终可用；阻塞的 Lock 调用阻止了新的读者获取该锁。
type RWMutex struct {
	w           Mutex  // held if there are pending writers
	writerSem   uint32 // 给写者的信号（semaphore），等待完成读者的操作
	readerSem   uint32 // 给读者的信号，等待完成写者的操作
	readerCount int32  // 等待的读者的数量
	readerWait  int32  // 离任的（departing）读者的数量
}

const rwmutexMaxReaders = 1 << 30

// Rlock 给 rw 上锁来进行读操作
//
// 它不应该用于嵌套得上读锁；一个阻塞的 Lock 调用排斥新的读者请求读锁。
// 见对 RWMutex 类型描述的文档
func (rw *RWMutex) RLock() {
	if race.Enabled {
		_ = rw.w.state
		race.Disable()
	}
	if atomic.AddInt32(&rw.readerCount, 1) < 0 {
		// 一个写者待定，等待。
		runtime_SemacquireMutex(&rw.readerSem, false, 0)
	}
	if race.Enabled {
		race.Enable()
		race.Acquire(unsafe.Pointer(&rw.readerSem))
	}
}

// RUlock 函数撤销一个单一的 RLock 调用；
// 它不会影响其他读者。
// 如果 rw 没有被读锁锁住，在此函数入口会报一个进行时错误。
func (rw *RWMutex) RUnlock() {
	if race.Enabled {
		_ = rw.w.state
		race.ReleaseMerge(unsafe.Pointer(&rw.writerSem))
		race.Disable()
	}
	if r := atomic.AddInt32(&rw.readerCount, -1); r < 0 {
		// Outlined slow-path to allow the fast-path to be inlined
		rw.rUnlockSlow(r)
	}
	if race.Enabled {
		race.Enable()
	}
}

func (rw *RWMutex) rUnlockSlow(r int32) {
	if r+1 == 0 || r+1 == -rwmutexMaxReaders {
		race.Enable()
		throw("sync: RUnlock of unlocked RWMutex")
	}
	// A writer is pending.
	if atomic.AddInt32(&rw.readerWait, -1) == 0 {
		// 上一个读者解除了对写者的阻塞。
		runtime_Semrelease(&rw.writerSem, false, 1)
	}
}

// Lock 函数将 rw 锁住用于读操作。
// 如果这个锁已经被读者或写者上锁，Lock 函数会阻塞直到锁可用。
func (rw *RWMutex) Lock() {
	if race.Enabled {
		_ = rw.w.state
		race.Disable()
	}
	// 首先，解决和其他写者的竞争
	rw.w.Lock()
	// 通知读者们这里有一个等待处理的写者。
	r := atomic.AddInt32(&rw.readerCount, -rwmutexMaxReaders) + rwmutexMaxReaders
	// 等待已激活的（active）读者
	if r != 0 && atomic.AddInt32(&rw.readerWait, r) != 0 {
		runtime_SemacquireMutex(&rw.writerSem, false, 0)
	}
	if race.Enabled {
		race.Enable()
		race.Acquire(unsafe.Pointer(&rw.readerSem))
		race.Acquire(unsafe.Pointer(&rw.writerSem))
	}
}

// Unlock 将 rw 解锁用于写操作。
// 如果 rw 未被上写锁，它会在入口报一个进行时的错误。
//
// 与 Mutex 相同，一个上锁的 RWMutex 不会关联到一个特定的协程。
// 一个协程可以 RLock 或 Lock 一个 RWMutex，而安排另一个协程 RUnlock 或 Unlock 它。
func (rw *RWMutex) Unlock() {
	if race.Enabled {
		_ = rw.w.state
		race.Release(unsafe.Pointer(&rw.readerSem))
		race.Disable()
	}

	// 通知读者们这有一个未活跃的写者。
	r := atomic.AddInt32(&rw.readerCount, rwmutexMaxReaders)
	if r >= rwmutexMaxReaders {
		race.Enable()
		throw("sync: Unlock of unlocked RWMutex")
	}
	// 解锁阻塞的读者，如果他们存在的话。
	for i := 0; i < int(r); i++ {
		runtime_Semrelease(&rw.readerSem, false, 0)
	}
	// 允许其他写者进行处理。
	rw.w.Unlock()
	if race.Enabled {
		race.Enable()
	}
}

// RLocker 返回一个定义了拥有 Lock 和 Unlock 两个方法的 Locker 接口。
// 通过Lock 和 Unlock 调用 rw.RLock 和 rw.RUnlock 函数。
func (rw *RWMutex) RLocker() Locker {
	return (*rlocker)(rw)
}

type rlocker RWMutex

func (r *rlocker) Lock()   { (*RWMutex)(r).RLock() }
func (r *rlocker) Unlock() { (*RWMutex)(r).RUnlock() }
