package examples

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
	"workpool"
)

type sleepWorkProducer int
type sleepWorkload int

var uptime int64
var collector chan [2]int64

func init() {
	uptime = time.Now().UnixNano() / 1e6
	rand.Seed(time.Now().Unix())
	collector = make(chan [2]int64, 100) // 用于收集各 work 执行的始末相对时间便于观测结果
}

func (w *sleepWorkload) Work() {
	start := time.Now().UnixNano()/1e6 - uptime
	time.Sleep(time.Duration(*w) * time.Millisecond)
	end := time.Now().UnixNano()/1e6 - uptime

	collector <- [2]int64{start, end}
	fmt.Printf("sleep %3dms, relative time: %5d to %5d\n", *w, start, end)
}
func (w *sleepWorkProducer) Produce() workpool.IWorkload {
	if *w <= 0 {
		return nil
	}
	*w--
	n := sleepWorkload(rand.Intn(200)) // 产生睡眠 200ms 内的 work
	return &n
}

// 测试方案：
//   用 sleepWorkload 来实现 IWorkload 接口，这个任务只用来做 sleep 任务并记录起始和结束的相对时间以及将这些信息发给收集器 collector
//   用 sleepWorkProducer 实现 IProducer 接口，用于生产固定数量的 sleepWorkload 任务
//   通过 collector 收集所有任务执行的起止时间（相对时间），用于观察。（可以通过绘图或者类似动态窗口的处理方式将这些数据做处理来观察结果，但未实现）
//   在程序函数中也有部分测试代码，如定时获取并发执行的任务数等。
func TestQuestion2(t *testing.T) {
	producer := new(sleepWorkProducer)
	*producer = 100
	// 开启收集执行信息任务
	wg := sync.WaitGroup{}
	wg.Add(1)
	collectTimeInfo := make([][2]int64, 0, *producer)
	// 启动收集执行时间信息
	go func() {
		for v := range collector {
			collectTimeInfo = append(collectTimeInfo, v)
		}
		wg.Done()
	}()

	Question2(producer)

	// 停止收集
	close(collector)
	wg.Wait()

	// 处理收集到的原始数据
	sort.Slice(collectTimeInfo, func(i, j int) bool {
		if collectTimeInfo[i][0] != collectTimeInfo[j][0] {
			return collectTimeInfo[i][0] < collectTimeInfo[j][0]
		}
		return collectTimeInfo[i][1] < collectTimeInfo[j][1]
	})
	for _, v := range collectTimeInfo {
		fmt.Println(v)
	}
	// TODO: 类似滑动窗口验证 collectTimeInfo (以毫秒精度记录的时间，有临界重复的问题)
	// 时间关系不再验证，参考一个输出案例 output.txt
}
