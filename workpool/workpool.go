package workpool

import (
	"context"
	"log"
	"time"
	"workpool/internal/sync"
)

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
type workerpool struct {
	workerCount       int                // 最大协程数目
	down              bool               // 标记是否已经下线
	ctx               context.Context    // 控制立即下线
	cancel            context.CancelFunc // 控制立即下线
	elasticJobBuf     *sync.ElasticBuf   // 带缓冲池的任务队列
	sync.ExtWaitGroup                    // 扩展了 WaitGroup
}

// NewWorkerpool 初始化固定协程数目 n 的工作池
func NewWorkerpool(n int) *workerpool {
	if n <= 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &workerpool{
		workerCount:   n,
		ctx:           ctx,
		cancel:        cancel,
		elasticJobBuf: sync.NewElasticBuf(),
	}
}

const (
	maxIdleDuration = 3 * time.Second
)

// define one worker's task: always process job
func (p *workerpool) spawnOneWorker() {
	defer p.Done()

	for {
		select {
		case job, ok := <-p.elasticJobBuf.Out:
			if !ok {
				return
			}
			if work, ok := job.(IWorkload); ok {
				work.Work()
			} else {
				log.Printf("Error: Unexpected job type %v\n", work)
			}
		case <-time.After(maxIdleDuration): // maxIdleDuration 内没有任务，自动收缩
			return
		case <-p.ctx.Done():
			return
		}
	}
}

// Start 开启工作池
func (p *workerpool) Start() {
	p.elasticJobBuf.Run(p.ctx)

	p.Add(1)
	go p.spawnOneWorker()
}

// Shutdown 优雅关闭工作池，保证所有工作处理完
func (p *workerpool) Shutdown() {
	if p.down {
		return
	}
	close(p.elasticJobBuf.In)
	p.down = true
}

// Down 立即下线
func (p *workerpool) Down() {
	if p.down {
		return
	}
	close(p.elasticJobBuf.In)
	p.cancel()
	p.down = true
}

// AddTask 非阻塞方式添加任务到工作池
func (p *workerpool) AddTask(work IWorkload) {
	if p.down {
		log.Println("Error: add task into closed pool")
		return
	}

	if p.GetWaitCount() == 0 {
		p.elasticJobBuf.In <- work
		go p.spawnOneWorker()
	} else {
		select {
		case p.elasticJobBuf.Out <- work: // 抢占进入输出队列
		default: // 若抢占失败，则进行队列中并尝试 spawn 新协程
			p.elasticJobBuf.In <- work
			if wc := p.GetWaitCount(); wc < uint64(p.workerCount) && p.CompareAndAdd(wc, 1) {
				go p.spawnOneWorker()
			}
		}
	}
}
