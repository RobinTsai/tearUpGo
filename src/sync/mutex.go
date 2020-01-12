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
	// 下面这四个常量相当于 mask，用来解析某值对应的位
	mutexLocked      = 1 << iota // 1 << 0 = 1，mutex 上了锁
	mutexWoken                   // 1 << 1 = 2。会 copy 上一句中的公式
	mutexStarving                // 1 << 2 = 4
	mutexWaiterShift = iota      // 3，一个数值右移三位后就是队列的数量。（iota是在遇见新的 const 时才会从 0 计数，这里只是不再用上面的公式，此时计数为 3）

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
	starvationThresholdNs = 1e6 // 成为饥饿状态的时间阈值：1ms
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
	starving := false // 标记是否在此线程内饥饿
	awoke := false    // 标记是否在此线程内唤醒
	iter := 0         // 用于判断是否可以自旋，（runtime/proc.go@sync_runtime_canSpin）
	old := m.state    // 调用 Lock 时，获取此时 state 的值，其包含了队列信息
	for {
		// 在饥饿模式中不自旋，拥有权会移交给等待者，所以我们不会再请求 mutex。
		if old&(mutexLocked|mutexStarving) == mutexLocked && runtime_canSpin(iter) { // 如果是上锁状态，且不是饥饿状态，且可以自旋
			// 主动自旋（Active spinning）是有意义的。
			// 尝试设置 mutexWoken 标志位以通知解锁。
			if !awoke && old&mutexWoken == 0 && old>>mutexWaiterShift != 0 && // 如果本线程未唤醒它，且未被其他线程唤醒，且队列中有等待者
				atomic.CompareAndSwapInt32(&m.state, old, old|mutexWoken) { // 则（本线程）唤醒它
				awoke = true // 标记为唤醒
			}
			runtime_doSpin() // 自旋，是为了不暂停线程，而再次去尝试获取锁
			iter++           // 自旋标记加 1
			old = m.state    // 自旋后，重新更新 old 状态
			continue
		}
		new := old
		// 不尝试去请求已是饥饿状态的 mutex，新来的协程必须等待。
		if old&mutexStarving == 0 { // old 不是饥饿状态
			new |= mutexLocked // new 标记为上锁
		}
		if old&(mutexLocked|mutexStarving) != 0 { // 如果 old 是饥饿状态或上锁状态
			new += 1 << mutexWaiterShift // 排队数量加 1
		}
		// 当前协程转换 mutex 到饥饿状态。
		// 但是如果 mutex 目前已被解锁，就不去转换。
		// Unlock 期望饥饿状态的 mutex 有一些等待者，这时这里不为 true。
		if starving && old&mutexLocked != 0 { // 如果当前线程饥饿，且 old 为上锁状态
			new |= mutexStarving // new 标记为饥饿状态
		}
		if awoke { // 如果它被标记为此线程唤醒
			// 这个协程已经从睡眠中唤醒，所以无论在哪一种情况下我们都需要重置标志位。
			if new&mutexWoken == 0 { // 如果 new 的 唤醒标志位 标记为 0 （未被唤醒）
				throw("sync: inconsistent mutex state")
			}
			new &^= mutexWoken // new & (^mutexWoken)，将 mutexWoken 对应位置为 0，表示此线程将它锁住
		} // 以上，则证明此锁已被唤醒，以下可以去抢占了。new 变量也已组装好了，只要 CAS 成功，就是抢占成功。
		if atomic.CompareAndSwapInt32(&m.state, old, new) { // 如果 m.state 还是 old（未变化），则更新为新状态 new
			if old&(mutexLocked|mutexStarving) == 0 { // 如果 old 即 未锁定 也 不是饥饿状态（已被此线程上锁）
				break // 退出 for，即不需要其他处理了
			} // 若 old （前一状态）是锁定的，或是饥饿的，还需要以下处理
			// 如果 waitStartTime 不为 0，排列到队列的前方。(LIFO: last in first out)
			queueLifo := waitStartTime != 0 // 如果 waitStartTime 标记此线程已经在执行了，则启用 LIFO 形式
			if waitStartTime == 0 {         // 如果 waitStartTime 为 0，首次获取此锁
				waitStartTime = runtime_nanotime() // 则记录获取锁的时间 waitStartTime。
			}
			runtime_SemacquireMutex(&m.sema, queueLifo)                                     // 根据 queueLifo 排到队列的前/后方
			starving = starving || runtime_nanotime()-waitStartTime > starvationThresholdNs // 是否已在饥饿状态，或等待时间大于 1ms
			old = m.state                                                                   // 重新获取 old
			if old&mutexStarving != 0 {                                                     // 如果 old 是 饥饿状态
				// 如果一个协程被唤醒，并且 mutex 在饥饿模式中，其拥有权被移交给我们，
				// 但 mutex 在某种程度上（in somewhat）处于非连续的状态：
				// mutexLocked 没有被设置并且我们仍然是一个等待者。修复它。
				if old&(mutexLocked|mutexWoken) != 0 || old>>mutexWaiterShift == 0 { // 如果 old 是饥饿或唤醒状态，或队列中无人排排队
					throw("sync: inconsistent mutex state")
				}
				delta := int32(mutexLocked - 1<<mutexWaiterShift)
				if !starving || old>>mutexWaiterShift == 1 { // 如果 不饥饿 或 队列数量为 1（自己）
					// 退出饥饿模式
					// 考虑到等待时间，在这里做这个操作非常关键。
					// 饥饿模式是如此的低效，因为一旦他们转换 mutex 到饥饿模式时，两个协程可能无限得锁步（lock-step）。
					delta -= mutexStarving
				}
				atomic.AddInt32(&m.state, delta)
				break
			}
			awoke = true // 标记为唤醒
			iter = 0
		} else { // 否则，重新取 m.state 为 old 状态
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
	new := atomic.AddInt32(&m.state, -mutexLocked) // 解锁，去除锁定状态
	if (new+mutexLocked)&mutexLocked == 0 {        // 如果 new 还是上锁状态（如果 m.state 本就未上锁会进入这里）
		throw("sync: unlock of unlocked mutex") // 异常
	}
	if new&mutexStarving == 0 { // 如果 new 不是饥饿状态
		old := new
		for {
			// 如果没有了等待者，或者一个协程已经被唤醒或拿到了锁，那就不需要去唤醒其他任何人。
			// 在饥饿模式下，拥有权直接从未上锁的协程移交给下一个等待者。
			// 既然之前我们在解锁这个 mutex 时，没有观察 mutexStarving，我们就不属于这个链路中的一部分，所以就结束（走开，get off the way）。
			if old>>mutexWaiterShift == 0 || old&(mutexLocked|mutexWoken|mutexStarving) != 0 { // old 如果没有等待者，或被锁/被唤醒/是饥饿
				return // 结束
			}
			// 拿到右侧以唤醒某一个。
			// Grab the right to wake someone.
			new = (old - 1<<mutexWaiterShift) | mutexWoken      // 队列 -1, 并重置 mutexWoken 标志位
			if atomic.CompareAndSwapInt32(&m.state, old, new) { // 如果 old 状态没变，重置此互斥量的状态为 new
				runtime_Semrelease(&m.sema, false)
				return
			}
			old = m.state // 否则重新获取此锁的状态，继续 for 循环
		}
	} else {
		// 饥饿模式：移交（handoff） mutex 的拥有权给下一个等待者。
		// 注意： mutexLocked 未被设置，等待者会在唤醒后设置它。
		// 但如果 mutexStarving 被设置了， mutex 仍然被认为是锁定的，因此新来的协程不会获得（acquire）它。
		runtime_Semrelease(&m.sema, true)
	}
}
