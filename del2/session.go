package del2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/gamexg/gomux"

	"github.com/gamexg/gomux/protocol"
	"github.com/gamexg/gotool/mem"
)

type Session struct {
	ctx      context.Context
	cancel   context.CancelFunc
	isServer bool

	rwcWriteMux sync.Mutex
	rwc         io.ReadWriteCloser

	// 0表示还未握手 或 握手失败，write等函数需要调用握手函数
	// 1表示已经成功完成握手
	// 原子操作
	handshakeStatus uint32

	// 握手完成时结束
	shakeHandsCtx    context.Context
	shakeHandsCancel context.CancelFunc
	shakeHandsM      sync.Mutex
	// 握手完成时设置为错误状态
	shakeHandsErr error

	shakeHandsMagicChan chan []byte
	shakeHandsPackChan  chan protocol.Pack

	StreamListRwm sync.RWMutex
	streamList    map[uint64]*gomux.Stream

	// loopRead 函数返回时终止
	readCtx       context.Context
	readCtxCancel context.CancelFunc
	// loopRead 错误信息，readCtx 结束前设置
	readErr error
}

func (s *Session) handshakeComplete() bool {
	return atomic.LoadUint32(&s.handshakeStatus) == 1
}

// 握手
func (s *Session) ShakeHands(ctx context.Context) (err error) {
	s.shakeHandsM.Lock()
	defer s.shakeHandsM.Unlock()

	// 测试是否已经完成握手
	select {
	case <-s.shakeHandsCtx.Done():
		return s.shakeHandsErr
	default:
		break
	}

	isServer := s.isServer
	shakeHandsCancel := s.shakeHandsCancel

	defer func() {
		s.shakeHandsErr = err
		shakeHandsCancel()
		if err != nil {
			s.rwc.Close()

			return
		}

		atomic.StoreUint32(&s.handshakeStatus, 1)
	}()

	go s.loopRead()

	switch isServer {
	case true:
		return s.shakeHandsServer(ctx)
	case false:
		return s.shakeHandsClient(ctx)
	}
	panic("不可能的错误")
}

func (s *Session) shakeHandsServer(ctx context.Context) error {
	rwc := s.rwc
	sCtx := s.ctx

	// 读取 协议魔数
	select {
	case <-sCtx.Done():
		return sCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readCtx.Done():
		return s.readErr
	case m := <-s.shakeHandsMagicChan:
		if bytes.Equal(m, protocol.MagicClient) == false {
			return fmt.Errorf("协议魔数 %#v 不正确", m)
		}
	}

	// 读取 hello
	select {
	case <-sCtx.Done():
		return sCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case pack := <-s.shakeHandsPackChan:
		hello, _ := pack.(*protocol.Hello)
		if hello == nil {
			return fmt.Errorf("%t 非预期的 heelo 包。", pack)
		}
		//以后实现协议版本判断
	}

	// 发送回应
	_, err := rwc.Write(protocol.MagicServer)
	if err != nil {
		return err
	}

	helloR := protocol.HelloR{
		ProtocolVersion: protocol.IVersion,
		LibraryVersion:  gomux.IVsersion,
		Status:          0,
		Delay:           0,
		Message:         "",
		Feature:         nil,
	}

	err = protocol.WritePack(rwc, &helloR, nil)
	if err != nil {
		return err
	}

	return nil
}

func (s *Session) shakeHandsClient(ctx context.Context) error {
	isServer := s.isServer

}

func (s *Session) writeFrame(frame protocol.Pack) error {
	rw := s.rwc

	buf := mem.Get(frame.Size())
	defer mem.Put(buf)

	n, err := frame.MarshalTo(buf)
	if err != nil {
		return err
	}

	data := buf[:n]

	_, err = protocol.WriteAll(rw, data)
	if err != nil {
		return err
	}

	return nil
}

// 负责循环读取包
func (s *Session) loopRead() {
	ctx := s.ctx
	cancel := s.cancel
	rwc := s.rwc
	isServer := s.isServer
	readCtxCancel := s.readCtxCancel

	defer rwc.Close()
	defer cancel()
	defer readCtxCancel()

	// 读取协议魔数
	var magic []byte

	switch isServer {
	case true:
		magic = make([]byte, len(protocol.MagicServer))
	case false:
		magic = make([]byte, len(protocol.MagicClient))
	}

	_, err := io.ReadFull(rwc, magic)
	if err != nil {
		s.readErr = fmt.Errorf("读取协议魔数错误, %v", err)
		return
	}

	select {
	case <-ctx.Done():
		return
	case s.shakeHandsMagicChan <- magic:
		break
	}

	magic = nil

	pack, _, buf, _, err := protocol.ReadPackAuto(rwc, nil, protocol.PackAuto)
	// todo 释放 buf
	_ = buf

	select {
	case <-ctx.Done():
		s.readErr = ctx.Err()
		return
	default:
		break
	}

	if err != nil {
		s.readErr = fmt.Errorf("ReadPackAuto, %v", err)
		return
	}

	for {
		pack, _, buf, _, err := protocol.ReadPackAuto(rwc, nil, protocol.PackAuto)
		// todo 释放 buf
		_ = buf

		select {
		case <-ctx.Done():
			s.readErr = ctx.Err()
			return
		default:
			break
		}

		if err != nil {
			s.readErr = fmt.Errorf("ReadPackAuto, %v", err)
			return
		}

		// todo 根据 流 id 决定转发到特定的流
		streamId := pack.GetStreamId()

		if streamId == 0 {

			s.shakeHands.handlePack(pack)
			continue
		}

		// 检查

	}
}

func (s *Session) WriteBytes(d []byte) (int, error) {
	s.rwc
}
