package gomux

import (
	"container/list"
	"sync"
)

// 连接列表
// 自动维护数组空间，自动扩容及缩小
type ConnList struct {
	m           sync.Mutex
	MaxSize     int
	DefaultSize int
	list        *list.List
}

func NwConnList(defaultSize int, maxSize int) *ConnList {
	return &ConnList{
		MaxSize:     maxSize,
		DefaultSize: defaultSize,
		list:        list.New(),
	}
}

func (c *ConnList) Put(v *Stream) {
	c.m.Lock()
	defer c.m.Unlock()

	c.list.PushBack(v)
}

func (c *ConnList) Get() *Stream {
	c.m.Lock()
	defer c.m.Unlock()

	e := c.list.Front()
	if e == nil {
		return nil
	}

	c.list.Remove(e)

	return e.Value.(*Stream)
}
