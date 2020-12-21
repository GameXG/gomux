package gomux

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/gamexg/goio"
	"github.com/gamexg/gomux/protocol"
)

type Session struct {
	ctx      context.Context
	cancel   context.CancelFunc
	c        io.ReadWriter
	conf     *Config
	isServer bool

	// 握手完成后收到的包
	packChan chan protocol.Pack

	// 握手状态
	// 握手后设置为 1,原子操作
	handshakeStatus    uint32
	handshakeMutex     sync.Mutex
	handshakeErr       error // error resulting from handshake
	handshakeMagicChan chan []byte
	handshakeHelloChan chan protocol.Pack
	handshakeErrChan   chan error

type Config struct {
}

type Stream struct {
	id uint64
}

func Clinet(ctx context.Context, c io.ReadWriter, conf *Config) *Session {
	lCtx, cancel := context.WithCancel(ctx)

	s := Session{
		ctx:                lCtx,
		cancel:             cancel,
		c:                  c,
		conf:               conf,
		isServer:           false,
		handshakeMagicChan: make(chan []byte, 1),
		handshakeHelloChan: make(chan protocol.Pack, 1),
		handshakeErrChan:   make(chan error, 1),
	}
	return &s
}

func Server(ctx context.Context, c io.ReadWriter, conf *Config) *Session {
	lCtx, cancel := context.WithCancel(ctx)

	s := Session{
		ctx:                lCtx,
		cancel:             cancel,
		c:                  c,
		conf:               conf,
		isServer:           true,
		handshakeMagicChan: make(chan []byte, 1),
		handshakeHelloChan: make(chan protocol.Pack, 1),
		handshakeErrChan:   make(chan error, 1),
	}
	return &s
}

func (s *Session) loopRead() {
	ctx := s.ctx
	cancel := s.cancel
	c := s.c
	isServer := s.isServer
	packChan := s.packChan

	defer cancel()

	var magicBuf []byte
	switch isServer {
	case true:
		magicBuf = make([]byte, len(protocol.MagicServer))
	case false:
		magicBuf = make([]byte, len(protocol.MagicClient))
	}

	_, err := io.ReadFull(c, magicBuf)
	if err != nil {
		select {
		case s.handshakeErrChan <- err:
			return
		default:
			return
		}
	}

	select {
	case <-ctx.Done():
		return
	default:
		break
	}

	select {
	case s.handshakeMagicChan <- magicBuf:
		break
	default:
		select {
		case s.handshakeErrChan <- fmt.Errorf("handshakeHelloChan 满"):
		default:
		}
		return
	}

	pack, _, _, _, err := protocol.ReadPackAuto(c, nil, protocol.PackAuto)

	select {
	case s.handshakeHelloChan <- pack:
		break
	default:
		select {
		case s.handshakeErrChan <- fmt.Errorf("handshakeHelloChan 满"):
		default:
		}
		return
	}

	for {
		// 接受包，并处理
		pack, _, _, _, err := protocol.ReadPackAuto(c, nil, protocol.PackAuto)

		select {
		case <-ctx.Done():
			return
		default:
			break
		}

		if err != nil {
			return
		}

		// todo 考虑怎么处理

		select {
		case packChan <- pack:
		case <-ctx.Done():
			return
		}
	}
}

// 完成握手
func (s *Session) Handshake(ctx context.Context) error {
	s.handshakeMutex.Lock()
	defer s.handshakeMutex.Unlock()

	if s.handshakeStatus != 0 {
		return s.handshakeErr
	}

	go s.loopRead()

	switch s.isServer {
	case true:
		err := s.handshakeServer(ctx)
		if err != nil {
			return err
		}
	case false:
		err := s.handshakeClient(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) handshakeServer(ctx context.Context) error {
	c := s.c
	sCtx := s.ctx

	// 协议魔数
	select {
	case clientMagic := <-s.handshakeMagicChan:
		if bytes.Equal(clientMagic, protocol.MagicClient) == false {
			return fmt.Errorf("协议魔数错误")
		}

	case <-ctx.Done():
		return ctx.Err()
	case <-sCtx.Done():
		return sCtx.Err()
	}

	// 接收 hello 包
	select {
	case pack := <-s.handshakeHelloChan:
		hello, _ := pack.(*protocol.Hello)
		if hello == nil {
			return fmt.Errorf("%#v 不是 hello 包", pack)
		}

	// 检查协议版本

	case <-ctx.Done():
		return ctx.Err()
	case <-sCtx.Done():
		return sCtx.Err()
	}

	helloR := protocol.HelloR{}
	err := protocol.WritePack(c, &helloR, nil)
	if err != nil {
		return err
	}

	return nil
}

func (s *Session) handshakeClient(ctx context.Context) error {
	c := s.c
	sCtx := s.ctx

	// 发送协议魔数
	_, err := goio.WriteAll(c, protocol.MagicClient)
	if err != nil {
		return err
	}

	// 发送 hello 包
	hello := protocol.Hello{
		ProtocolVersion: protocol.IVersion,
		LibraryVersion:  IVsersion,
		//  以后可以考虑实现特征协商，目前并未实现
		Feature: nil,
	}
	err = protocol.WritePack(c, &hello, nil)
	if err != nil {
		return err
	}

	// 读取协议魔数
	select {
	case magicServer := <-s.handshakeMagicChan:
		if bytes.Equal(magicServer, protocol.MagicServer) {
			return fmt.Errorf("unexpected agreement magic number %v", magicServer)
		}

	case <-ctx.Done():
		return ctx.Err()
	case <-sCtx.Done():
		return sCtx.Err()
	}

	// 读取 helloR 包
	select {
	case pack := <-s.handshakeHelloChan:
		helloR, _ := pack.(*protocol.HelloR)
		if helloR == nil {
			return fmt.Errorf("%#v 不是 helloR 包", pack)
		}

		if helloR.Status != 0 {
			return fmt.Errorf("status:%v message:%v", helloR.Status, helloR.Message)
		}

	case <-ctx.Done():
		return ctx.Err()
	case <-sCtx.Done():
		return sCtx.Err()
	}

	return nil
}

func (s *Session) OpenStream() (*Stream, error) {
	//发送 StreamNew 包即可

	return &Stream{}, nil
}

func (s *Session) AcceptStream() error {
	panic("implement me")
}

func (s *Stream) Write(i []byte) (int, error) {
	panic("implement me")
}

func (s *Stream) Read(i []byte) (int, error) {
	panic("implement me")
}

func (s *Stream) Close() error {
	panic("implement me")
}

func (s *Stream) CloseWrite() error {
	panic("implement me")
}

func (s *Stream) SetDeadline(t interface{}) error {
	panic("implement me")
}

func (s *Stream) SetReadDeadline(t interface{}) error {
	panic("implement me")
}

func (s *Stream) SetWriteDeadline(t interface{}) error {
	panic("implement me")
}

func (s *Stream) Id() uint64 {
	return s.id
}

func (s *Stream) Session() Session {
	panic("implement me")
}

func (s *Stream) RemoteAddr() interface{} {
	panic("implement me")
}

func (s *Stream) LocalAddr() interface{} {
	panic("implement me")
}
