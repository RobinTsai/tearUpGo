// 本来是个面试题，我实现了一个协程池来实现，将原来的题目作为了 example 放在这里
package examples

import (
	"fmt"
	"time"
	"workpool"
)

/*
// IWorkload 请勿修改接口
type IWorkload interface {
	// Work内包含一些耗时的处理，可能是密集计算或者外部IO
	Work()
}

// IProducer 请勿修改接口
type IProducer interface {
	// Produce每次调用会返回一个IWorkload实例
	// 当返回nil时表示已经生产完毕
	Produce() IWorkload
}
*/

// 问题2：请编写函数Question2的实现如下功能
// 该函数输入一个IProducer实例，每次调用其Produce()方法会返回一个IWorkload实例。
// 1. 请反复调用该Produce()方法，直到返回nil，表明没有更多IWorkload。
//    此间可能会生产大量IWorkload实例，数目在此未知。
// 2. 对每个生产出的IWorkload实例，请调用一次它的Work()方法。
//    Work()内包含一些耗时的处理，可能是密集计算或者外部IO。
// 3. 请并发调用多个IWorkload的Work()方法，最多允许5个并发的Work()执行。
//    单个并发的实现，或并发数超过5的限制，都不能得分。
//
// 提示：请最小化内存、CPU代价
// 提示：请尽量使用规范的代码风格，使代码整洁易读
// 提示：如果也实现了测试代码，请一并提交，将有利于分数评定

// ------------------------------------------------------------------------

// 解答思路：
//   一个未采用的方案：此场景用扩展库的 Semaphore 很合适。
//
//   下面是用协程池的方案来做此题，经过逐步优化，最后如下实现
//   功能点：
//     1. 优雅关闭工作池（会等待所有任务执行完）
//     2. 立即关闭工作池
//     3. 动态伸缩协程个数，最少 0 个，最多 workerpool.workerCount 个协程
//     4. 弹性池保存任务列表
//     5. 集成并扩展 WaitGroup，等待所有任务任务处理结束，执行期间可查看 WaitGroup 中存在个数
func Question2(producer workpool.IProducer) {
	pool := workpool.NewWorkerpool(5)
	pool.Start()

	go func() { // 测试代码：定时查看协程个数
		t := time.NewTicker(time.Second)
		defer t.Stop()

		for range t.C {
			fmt.Println("cur worker count:", pool.GetWaitCount())
		}
	}()

	taskCount := 0

	workload := producer.Produce()
	for workload != nil {
		taskCount++ // 用于测试
		pool.AddTask(workload)
		workload = producer.Produce()
	}
	fmt.Println("total work count:", taskCount)

	pool.Shutdown()
	pool.Wait()

	fmt.Println("worker count at the end:", pool.GetWaitCount())
	// fmt.Println("pool buf len at the end:", pool.elasticJobBuf.Len()) // 测试用
}
