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
	// NewSession 传入的
	externalCtx context.Context

	ctx    context.Context
	cancel context.CancelFunc

	// 关闭操作有如下来源：
	// NewSession 传入的上级 ctx 关闭
	// loopRead 操作读取时发现连接关闭
	// 写操作函数，操作失败，并会影响 session 状态时
	// Close 函数被调用
	closeM         sync.Mutex
	closeCtx       context.Context
	closeCtxCancel context.CancelFunc
	closeErr       error

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

	/*
		// loopRead 函数返回时终止
		readCtx       context.Context
		readCtxCancel context.CancelFunc
		// loopRead 错误信息，readCtx 结束前设置
		readErr error*/

	// 传入需要向 rwc 发送的数据
	writeChan chan []byte
}

func (s *Session) handshakeComplete() bool {
	return atomic.LoadUint32(&s.handshakeStatus) == 1
}

// 关闭会话
// 内部使用，多次调用只会保存第一个错误
// 负责关闭所有 ctx、链接、资源等
func (s *Session) close(err error) {

}

// 握手
func (s *Session) ShakeHands(ctx context.Context) (err error) {
	s.shakeHandsM.Lock()
	defer s.shakeHandsM.Unlock()

	// 是否已经完成了握手
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
	go s.loopWrite()

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

// 负责循环读取包
func (s *Session) loopRead() {
	ctx := s.ctx
	cancel := s.cancel
	rwc := s.rwc
	isServer := s.isServer

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

// 循环写线程
func (s *Session) loopWrite() {
	ctx := s.ctx
	rwm := s.rwc

	writeChan := s.writeChan

	for {
		select {
		case <-ctx.Done():
			s.close(ctx.Err())
			return
		case data := <-writeChan:
			_, err := protocol.WriteAll(rwm, data)
			mem.Put(data)
			if err != nil {
				s.close(err)
			}
		}
	}
}
