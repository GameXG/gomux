package gomux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

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

	rwc io.ReadWriteCloser

	shakeHandsM sync.Mutex
	// 0表示还未握手 或 握手失败，write等函数需要调用握手函数
	// 1表示已经成功完成握手
	// 原子操作
	handshakeStatus uint32
	// 握手完成时 ctx 结束
	shakeHandsCtx    context.Context
	shakeHandsCancel context.CancelFunc
	// 握手完成时设置为错误状态
	shakeHandsErr error

	shakeHandsMagicChan chan []byte
	shakeHandsPackChan  chan protocol.Pack

	// 流id
	// 客户端为单数，服务端为双数。
	// 客户端从 1 开始，服务端从 2开始。
	nextStreamId  uint64
	StreamListRwm sync.RWMutex
	streamList    map[uint64]*Stream

	// 传入需要向 rwc 发送的数据
	writeChan chan []byte

	// 等待接受的连接数量
	acceptStreamSize int32
	// 受限制的追打队列大小
	acceptStreamMaxSize int32
	// 等待接受的连接，缓冲区尺寸必须大于  acceptStreamMaxSize
	acceptStream chan *Stream
}

// 握手是否成功完成
// 握手成功返回 true
// 未握手、握手失败都会返回 false
// 握手失败时，详细错误信息由 ShakeHands 返回
func (s *Session) handshakeComplete() bool {
	return atomic.LoadUint32(&s.handshakeStatus) == 1
}

// 关闭会话
// 内部使用，多次调用只会保存第一个错误
// 负责关闭所有 ctx、链接、资源等
func (s *Session) close(err error) {
	select {
	case <-s.closeCtx.Done():
		return
	default:
		break
	}

	s.closeM.Lock()
	defer s.closeM.Unlock()

	select {
	case <-s.closeCtx.Done():
		return
	default:
		break
	}

	s.closeErr = err
	s.closeCtxCancel()
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
			s.close(err)
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
	err := s.writeBytes(ctx, protocol.MagicServer)
	if err != nil {
		return err
	}

	helloR := protocol.HelloR{
		ProtocolVersion: protocol.IVersion,
		LibraryVersion:  IVsersion,
		Status:          0,
		Delay:           0,
		Message:         "",
		Feature:         nil,
	}

	err = s.writePack(ctx, &helloR, nil)
	if err != nil {
		return err
	}

	return nil
}

func (s *Session) shakeHandsClient(ctx context.Context) error {
	sCtx := s.ctx

	err := s.writeBytes(ctx, protocol.MagicClient)
	if err != nil {
		return err
	}

	hello := protocol.Hello{
		ProtocolVersion: protocol.IVersion,
		LibraryVersion:  IVsersion,
		Feature:         nil,
	}

	err = s.writePack(ctx, &hello, nil)
	if err != nil {
		return err
	}

	select {
	case <-sCtx.Done():
		return sCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case magic := <-s.shakeHandsMagicChan:
		if bytes.Equal(magic, protocol.MagicServer) == false {
			return fmt.Errorf("magic 错误")
		}
	}

	select {
	case <-sCtx.Done():
		return sCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case pack := <-s.shakeHandsPackChan:
		helloR, _ := pack.(*protocol.HelloR)
		if helloR == nil {
			return fmt.Errorf("包类型 %t 不是 haelloR", pack)
		}

		if helloR.Status != 0 {
			return fmt.Errorf("Status:%v Message:%v", helloR.Status, helloR.Message)
		}
	}

	return nil
}

// 负责循环读取包
func (s *Session) loopRead() {
	ctx := s.ctx
	rwc := s.rwc

	err := s.shakeHandsRead()
	if err != nil {
		s.close(err)
		return
	}

	for {
		func() {

			pack, _, buf, data, err := protocol.ReadPackAuto(rwc, nil, protocol.PackAuto)
			// todo 释放 buf
			_ = buf

			select {
			case <-ctx.Done():
				s.close(ctx.Err())
				return
			default:
				break
			}

			if err != nil {
				s.close(fmt.Errorf("ReadPackAuto, %v", err))
				return
			}

			streamId := pack.GetStreamId()

			if streamId == 0 {
				return
			}

			// 检查 stream 是否存在

			// New 创建
			// data 数据
			// close 关闭
			// rst 强制关闭

			switch pack.GetPackType() {
			case protocol.PackTypeStreamNew:
				s.addStream(pack)

			case protocol.PackTypeStreamData, protocol.PackTypeStreamDown:
				// 查找对应的 stream
				// 		不存在，则发出 rst
				//		存在，则调用对应 stream 的处理函数
				s.StreamListRwm.RLock()
				defer s.StreamListRwm.RUnlock()
				stream, ok := s.streamList[streamId]
				if !ok {
					s.writeRstPack(context.Background(), streamId)
					return
				}

				stream.handlePack(pack, data)

			case protocol.PackTypeStreamClose, protocol.PackTypeStreamRst:
				// 检查对应的流是否存在
				// 	存在则关闭，不存在则不处理
				s.StreamListRwm.Lock()
				defer s.StreamListRwm.Unlock()

				stream, ok := s.streamList[streamId]
				if !ok {
					return
				}

				switch pack.GetPackType() {
				case protocol.PackTypeStreamClose:
					stream.close()
				case protocol.PackTypeStreamRst:
					stream.rst()
				default:
					panic("非预期的包类型")
				}

			default:
				// 收到非预期的类型，跳过当钱 包
				return
			}
		}()
	}
}

// 创建流
// pack
func newStream(ctx context.Context, session *Session, id uint64) *Stream {
	lCtx, cancel := context.WithCancel(ctx)

	s := Stream{
		ctx:     lCtx,
		cancel:  cancel,
		session: session,
		Id:      id,
	}
	return &s
}

func (s *Session) shakeHandsRead() error {
	ctx := s.ctx
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
		return fmt.Errorf("读取协议魔数错误, %v", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.shakeHandsMagicChan <- magic:
		break
	}

	// 读取 hello 或 helloR 包
	pack, _, buf, _, err := protocol.ReadPackAuto(rwc, nil, protocol.PackAuto)
	// todo 释放 buf
	_ = buf

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		break
	}

	if err != nil {
		return fmt.Errorf("ReadPackAuto, %v", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.shakeHandsPackChan <- pack:
		return nil
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

func (s *Session) writePack(ctx context.Context, pack protocol.Pack, data []byte) error {
	buf, err := protocol.WritePack2Bytes(nil, pack, data)
	if err != nil {
		return err
	}

	// todo 释放 buf 到内存池

	err = s.writeBytes(ctx, buf)
	if err != nil {
		return err
	}

	return nil
}

// 写包
// 写入到 writeChan 即返回
func (s *Session) writeBytes(ctx context.Context, d []byte) error {
	sCtx := s.ctx
	select {
	case <-sCtx.Done():
		return sCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.writeChan <- d:
		return nil
	}
}

func (s *Session) addStream(pack protocol.Pack) {
	streamId := pack.GetStreamId()

	size := atomic.AddInt32(&s.acceptStreamSize, 1)
	if size > s.acceptStreamMaxSize {
		// 等待中的连接数超出上限。
		atomic.AddInt32(&s.acceptStreamMaxSize, -1)
		_ = s.writeRstPack(context.Background(), streamId)
		return
	}

	// 需要新建 stream
	// 持有锁
	// 检查是否已经存在，
	//		已经存在则 rst 掉这个 stream
	// 创建流
	// 添加进 map，添加进待接受连接列表
	s.StreamListRwm.Lock()
	defer s.StreamListRwm.Unlock()

	stream, ok := s.streamList[streamId]
	if ok {
		_ = s.writeRstPack(context.Background(), streamId)
		stream.rst()
		return
	}

	stream = newStream(nil, s, streamId)

	s.streamList[stream.Id] = stream

	// 添加到 待接受 连接列表
	select {
	case s.acceptStream <- stream:
	case <-s.ctx.Done():
		return
	}
}

func (s *Session) writeRstPack(ctx context.Context, id uint64) error {
	rst := protocol.StreamRst{
		Id: id,
	}

	err := s.writePack(ctx, &rst, nil)
	if err != nil {
		return err
	}

	return nil
}

// 新建一个新流
// 参数：
//		ctx		newStream 超时控制的 ctx，而不是 stream 生命周期 ctx
func (s *Session) NewStream(ctx context.Context) (*Stream, error) {
	// 检查是否握手
	if s.handshakeComplete() == false {
		err := s.ShakeHands(ctx)
		if err != nil {
			return nil, err
		}
	}

	// 创建 stream
	// 发送 streamnew 包
	// 返回成功

	id := atomic.AddUint64(&s.nextStreamId, 2)
	if s.isServer == false {
		id--
	}

	stream := newStream(s.ctx, s, id)

	streamNew := protocol.StreamNew{
		Id: id,
	}

	err := s.writePack(ctx, &streamNew, nil)
	if err != nil {
		return nil, err
	}

	s.StreamListRwm.Lock()
	defer s.StreamListRwm.Unlock()

	s.streamList[id] = stream

	return stream, nil
}

func (s *Session) AcceptContext(ctx context.Context) (*Stream, error) {
	// 检查是否握手
	if s.handshakeComplete() == false {
		err := s.ShakeHands(ctx)
		if err != nil {
			return nil, err
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()

	case <-s.ctx.Done():
		return nil, s.ctx.Err()

	case stream := <-s.acceptStream:
		return stream, nil
	}
}
