package gomux

import (
	"context"

	"github.com/gamexg/gomux/protocol"
)

type Stream struct {
	ctx    context.Context
	cancel context.CancelFunc

	Id      uint64
	session *Session
}

type StreamStatus int

const (
	StreamStatusNone StreamStatus = iota
	// 创建中
	StreamStatusCreating
	StreamStatusCreated
	StreamStatusClosed
	// 出错了
	StreamStatusErr
	StreamStatusRst
)

// 复位连接
// 仅仅 stream 关闭部分，不需要通知 session
func (s *Stream) rst() {

}

// 从远端发送过来的包
// 目前只会是 data 和 down 包
func (s *Stream) handlePack(pack protocol.Pack, data []byte) {
	// 填充到本地缓冲区

	switch pack.GetPackType() {
	case protocol.PackTypeStreamData:
		dataPack, ok := pack.(*protocol.StreamData)
		if !ok {
			return
		}

	case protocol.PackTypeStreamDown:
	default:
		// todo 非预期的包类型
	}

}

// 关闭流
// 仅仅 stream 关闭部分，不需要通知 session
func (s *Stream) close() {
	s.cancel()
}
