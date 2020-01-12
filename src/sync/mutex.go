// sync 包提供了基本的 **同步原语** (synchronization primitives) 例如 **互斥锁**（mutual exclusion locks）。
// 除了 Once 和 WaitGroup 类型，很多是为了低级库的常规使用。
// 高级别的同步最好用通道（channels）和通信（communications）来做。
//
// 在这个包下定义的变量或类型不应该被复制使用。
package sync

import (
	"internal/race"
	"sync/atomic"
	"unsafe"
)

func throw(string) // 在 runtime 包下提供了

// 一个 Mutex 类型是一个互斥锁（mutual exclusion lock）。
// Mutex 的零值是一个未被锁住的互斥量。
// 一个 Mutex 在第一次被使用后一定不要被复制。
type Mutex struct {
	state int32
	sema  uint32
}

// 一个 Locker 表示一个可以被上锁和解锁的对象。
type Locker interface {
	Lock()
	Unlock()
}

const (
	mutexLocked      = 1 << iota // 1 << 0 = 1，mutex 上了锁
	mutexWoken                   // 1 << 1 = 2
	mutexStarving                // 1 << 2 = 4
	mutexWaiterShift = iota      // 0

	// Mutex（互斥量）是公平的.
	//
	// Mutex 可以有 2 个操作模式： **正常** 和 **饥饿** 模式。
	// 在正常模式下，等待者会在一个 FIFO 队列中排队，但一个被唤醒的等待者不会拥有这个　mutex，并且要和新到来的协程竞争 mutex 的拥有权。
	// 新到来的协程是有优势的：他们已经在 CPU 上运行了，而且可能有很多协程，所以一个被唤醒的等待者有更大的机会竞争失败。
	// 在这种情况下，它在等待队列中排在最前面。如果一个等待者没有请求到 mutex 超过了 1 毫秒，它将转换为 **饥饿** 模式。
	//
	// 在饥饿模式中，mutex 的拥有权会直接从没有上锁的协程（the unlocking goroutine）中移交给队列中的第一个等待者。
	// 新到来的协程不会再试图获取这个 mutex，即便它将要被解锁，并且不会自旋（spin）。
	// 取而代之的是，他们会排列在等待队列的末尾。
	//
	// 如果一个等待者拿到了这个 mutex 的拥有权，而且下面的任意一个条件
	// - 1. 它是队列中的最后一个等待者
	// - 2. 它的等待时间少于 1 毫秒
	// 那么，这个 mutex 就会从 饥饿 状态转换为 正常 状态。
	//
	// 正常模式有相当好的性能，因为即便存在多个被阻塞的等待者，一个协程可以连续得请求 mutex 多次。
	// 饥饿模式对阻止队尾延迟（tail latency）的病态情况（pathological cases）很重要。
	starvationThresholdNs = 1e6
)

// Lock 方法会将 m 上锁。
// 如果这个 锁 已经被使用，这个调用的协程将会阻塞直到这个 mutex 可用。
func (m *Mutex) Lock() {
	// 快路径（fast path）：拿到未上锁的 mutex。
	if atomic.CompareAndSwapInt32(&m.state, 0, mutexLocked) {
		if race.Enabled {
			race.Acquire(unsafe.Pointer(m))
		}
		return
	}

	var waitStartTime int64
	starving := false
	awoke := false
	iter := 0
	old := m.state
	for {
		// 在饥饿模式中不自旋，拥有权会移交给等待者，所以我们不会再请求 mutex。
		if old&(mutexLocked|mutexStarving) == mutexLocked && runtime_canSpin(iter) {
			// 主动自旋（Active spinning）是有意义的。
			// 尝试设置 mutexWoken 标志位以通知解锁。
			if !awoke && old&mutexWoken == 0 && old>>mutexWaiterShift != 0 &&
				atomic.CompareAndSwapInt32(&m.state, old, old|mutexWoken) {
				awoke = true
			}
			runtime_doSpin()
			iter++
			old = m.state
			continue
		}
		new := old
		// 不尝试去请求已是饥饿状态的 mutex，新来的协程必须排除。
		if old&mutexStarving == 0 {
			new |= mutexLocked
		}
		if old&(mutexLocked|mutexStarving) != 0 {
			new += 1 << mutexWaiterShift
		}
		// 当前协程转换 mutex 到饥饿状态。
		// 但是如果 mutex 目前已被解锁，就不去转换。
		// Unlock 期望饥饿状态的 mutex 有一些等待者，这时这里不为 true。
		if starving && old&mutexLocked != 0 {
			new |= mutexStarving
		}
		if awoke {
			// 这个协程已经从睡眠中唤醒，所以无论在哪一种情况下我们都需要重置标志位。
			if new&mutexWoken == 0 {
				throw("sync: inconsistent mutex state")
			}
			new &^= mutexWoken
		}
		if atomic.CompareAndSwapInt32(&m.state, old, new) {
			if old&(mutexLocked|mutexStarving) == 0 {
				break // 用 CAS 销售此 mutex
			}
			// 如果我们之前已经等待过，排列到队列的前方。
			queueLifo := waitStartTime != 0
			if waitStartTime == 0 {
				waitStartTime = runtime_nanotime()
			}
			runtime_SemacquireMutex(&m.sema, queueLifo)
			starving = starving || runtime_nanotime()-waitStartTime > starvationThresholdNs
			old = m.state
			if old&mutexStarving != 0 {
				// 如果一个协程被唤醒，并且 mutex 在饥饿模式中，其拥有权被移交给我们，但 mutex 在某种程度上（in somewhat）处于非连续的状态：
				// mutexLocked 没有被设置并且我们仍然是一个等待者。修复它。
				if old&(mutexLocked|mutexWoken) != 0 || old>>mutexWaiterShift == 0 {
					throw("sync: inconsistent mutex state")
				}
				delta := int32(mutexLocked - 1<<mutexWaiterShift)
				if !starving || old>>mutexWaiterShift == 1 {
					// 退出饥饿模式
					// 考虑到等待时间，在这里做这个操作非常关键。
					// 饥饿模式是如此的低效，因为一旦他们转换 mutex 到饥饿模式时，两个协程可能无限得锁步（lock-step）。
					delta -= mutexStarving
				}
				atomic.AddInt32(&m.state, delta)
				break
			}
			awoke = true
			iter = 0
		} else {
			old = m.state
		}
	}

	if race.Enabled {
		race.Acquire(unsafe.Pointer(m))
	}
}

// Unlock 将 m 解锁。
// 如果 m 没有被上锁，在调用 Unlock 时会有个进行时错误（run-time error）。
//
// 被上锁的 mutex 与特定的协程无关。也就是说，一个协程锁住一个 mutex，然后安排另一个去解锁是允许的。
func (m *Mutex) Unlock() {
	if race.Enabled {
		_ = m.state
		race.Release(unsafe.Pointer(m))
	}

	// 快路径（fast path）：去掉锁定的位。
	new := atomic.AddInt32(&m.state, -mutexLocked)
	if (new+mutexLocked)&mutexLocked == 0 {
		throw("sync: unlock of unlocked mutex")
	}
	if new&mutexStarving == 0 {
		old := new
		for {
			// 如果没有了等待者，或者一个协程已经被唤醒或拿到了锁，那就不需要去唤醒其他任何人。
			// 在饥饿模式下，拥有权直接从未上锁的协程移交给下一个等待者。
			// 既然之前我们在解锁这个 mutex 时，没有观察 mutexStarving，我们就不属于这个链路中的一部分，所以就结束（走开，get off the way）。
			if old>>mutexWaiterShift == 0 || old&(mutexLocked|mutexWoken|mutexStarving) != 0 {
				return
			}
			// 拿到右侧以唤醒某一个。
			// Grab the right to wake someone.
			new = (old - 1<<mutexWaiterShift) | mutexWoken
			if atomic.CompareAndSwapInt32(&m.state, old, new) {
				runtime_Semrelease(&m.sema, false)
				return
			}
			old = m.state
		}
	} else {
		// 饥饿模式：移交（handoff）mutex 拥有权给下一个等待者。
		// 注意： mutexLocked 未被设置，等待者会在唤醒后设置它。
		// 但如果 mutexStarving 被设置了， mutex 仍然被认为是锁定的，因此新来的协程不会获得（acquire）它。
		runtime_Semrelease(&m.sema, true)
	}
}
