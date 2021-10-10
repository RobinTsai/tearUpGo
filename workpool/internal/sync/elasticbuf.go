package sync

import "context"

const (
	defaultChanSize = 2
)

type ElasticBuf struct {
	In, Out chan interface{}
	buf     []interface{}
}

func NewElasticBuf() *ElasticBuf {
	return &ElasticBuf{
		In:  make(chan interface{}, defaultChanSize),
		Out: make(chan interface{}, defaultChanSize),
	}
}

func (eb *ElasticBuf) Len() int {
	return len(eb.buf)
}

// ctx 用于立即关闭 eb 的处理
// 关闭 eb.In 时为优雅关闭——会将所有存在 buf 中的信息都从 Out 中读走再结束 eb
func (eb *ElasticBuf) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background() // 永远不会主动结束
	}

	run := func() {
		for {
			if len(eb.buf) > 0 {
				select {
				case e, ok := <-eb.In:
					if !ok { // In 关闭，将 In 设置为 nil，即永久阻塞，以便将所有数据都写给 Out
						eb.In = nil
						break
					}
					eb.buf = append(eb.buf, e)
				case eb.Out <- eb.buf[0]:
					eb.buf = eb.buf[1:]
				case <-ctx.Done():
					return
				}
			} else {
				select {
				case e, ok := <-eb.In:
					if !ok { // In 已经关闭，且此时所有 buf 数据已读完，则关闭 Out
						close(eb.Out)
						return
					}
					eb.buf = append(eb.buf, e)
				case <-ctx.Done():
					return
				}
			}
		}
	}

	go run()
}
