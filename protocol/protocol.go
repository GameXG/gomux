package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	math_bits "math/bits"

	"github.com/gamexg/go-mempool"
)

// 协议设计
// 单路复用协议
// 需求：
// 		实现单路复用功能
//		单个 stream 阻塞不会影响其他 stream
//		读写超时
//		尽可能的小资源占用
//			包括内存
//			cpu
//			网络流量

// 包格式
// 包整体长度  uint16
// 包头长度 Uvarint
// 		包类型  Uvarint
// 		序列化后的包数据
// 附加数据

// 包类型
// hello
// helloR
// 用来做协议握手，确认两端协议版本号、支持的功能

// 流创建
// 流数据
// 流关闭
// 流 rst

// 窗口剩余空间通知(直至某个id的包)

// 协调增减窗口尺寸

// ping 包
// pong 包

// 协议版本
var IVersion uint64 = 0x0000000000000001

// 协议魔数
var MagicClient = []byte("gomux-c")
var MagicServer = []byte("gomux-s")

type PackMarshalTo interface {
	MarshalTo(dAtA []byte) (int, error)
	GetPackType() PackType
	Size() int
	GetStreamId() uint64
}

type PackUnmarshal interface {
	Unmarshal(dAtA []byte) error
	GetPackType() PackType
	GetStreamId() uint64
}

type Pack interface {
	PackMarshalTo
	PackUnmarshal
}

type Mempool interface {
	Get(l int) []byte
	Put([]byte)
}

var DefaultMemPool = new(memPool)

type memPool struct {
}

func (p *memPool) Get(size int) []byte {
	return mempool.Get(size)
}

func (p *memPool) Put(v []byte) {
	_ = mempool.Put(v)
}

// 计算 uvarint 序列化后需要多少字节
func UvarintSize(x uint64) (n int) {
	return (math_bits.Len64(x|1) + 6) / 7
}

func WriteAll(w io.Writer, buf []byte) (n int, err error) {
	b := buf
	for len(b) != 0 {
		ln, lerr := w.Write(b)

		n += ln
		b = b[ln:]

		if lerr != nil {
			return n, lerr
		}
	}
	return
}

func WritePack(writer io.Writer, pack PackMarshalTo, data []byte) error {
	packProtoSize := pack.Size()
	packHeadSize := UvarintSize(uint64(pack.GetPackType())) + packProtoSize
	packSize := UvarintSize(uint64(packHeadSize)) + packHeadSize + len(data)
	bufSize := 2 + packSize

	if packSize > 0xFFFE {
		return fmt.Errorf("pack 尺寸 %v 太大", packSize)
	}

	buf := mempool.Get(bufSize)
	defer mempool.Put(buf)

	index := 0

	binary.BigEndian.PutUint16(buf, uint16(packSize))
	index += 2

	index += binary.PutUvarint(buf[index:], uint64(packHeadSize))
	index += binary.PutUvarint(buf[index:], uint64(pack.GetPackType()))

	n, err := pack.MarshalTo(buf[index:])
	if err != nil {
		return err
	}
	index += n

	index += copy(buf[index:], data)

	if index != bufSize {
		return fmt.Errorf("内部尺寸错误")
	}

	_, err = WriteAll(writer, buf)
	if err != nil {
		return err
	}

	return nil
}

func WritePack2Bytes(m Mempool, pack PackMarshalTo, data []byte) ([]byte, error) {
	if m == nil {
		m = DefaultMemPool
	}

	packProtoSize := pack.Size()
	packHeadSize := UvarintSize(uint64(pack.GetPackType())) + packProtoSize
	packSize := UvarintSize(uint64(packHeadSize)) + packHeadSize + len(data)
	bufSize := 2 + packSize

	if packSize > 0xFFFE {
		return nil, fmt.Errorf("pack 尺寸 %v 太大", packSize)
	}

	buf := m.Get(bufSize)

	index := 0

	binary.BigEndian.PutUint16(buf, uint16(packSize))
	index += 2

	index += binary.PutUvarint(buf[index:], uint64(packHeadSize))
	index += binary.PutUvarint(buf[index:], uint64(pack.GetPackType()))

	n, err := pack.MarshalTo(buf[index:])
	if err != nil {
		m.Put(buf)
		return nil, err
	}
	index += n

	index += copy(buf[index:], data)

	if index != bufSize {
		m.Put(buf)
		return nil, fmt.Errorf("内部尺寸错误")
	}

	return buf, nil
}

// 读取包到缓冲区
// 返回值：
//	PackType	包类型
//	[]byte		包全部数据，即从 Mempool 申请的空间已使用的部分。内存使用完毕时，可以使用 mp.Put 释放回内存池
//	[]byte		序列化后的包数据
//	[]byte		附加数据
//	error
func ReadPackBuf(r io.Reader, mp Mempool) (PackType, []byte, []byte, []byte, error) {
	if mp == nil {
		mp = DefaultMemPool
	}

	sizeBuf := make([]byte, 2)

	_, err := io.ReadFull(r, sizeBuf)
	if err != nil {
		return 0, nil, nil, nil, err
	}

	packSize := binary.BigEndian.Uint16(sizeBuf)

	buf := mp.Get(int(packSize))
	_, err = io.ReadFull(r, buf)
	if err != nil {
		mp.Put(buf)
		return 0, nil, nil, nil, err
	}

	packHeadSize, n := binary.Uvarint(buf)
	if n > int(packSize) {
		// 除非标准库出现问题，不然不会这样
		return 0, buf, nil, nil, fmt.Errorf("内部错误， n %v > packSize %v", n, packSize)
	}

	if packHeadSize > (uint64(packSize) - uint64(n)) {
		return 0, buf, nil, nil, fmt.Errorf("packHeadSize (%v) > ( packSize %v - n %v )。", packHeadSize, packSize, n)
	}

	packType, n2 := binary.Uvarint(buf[n:])
	if n2 > int(packSize)-n {
		// 除非标准库出现问题，不然不会这样
		return 0, buf, nil, nil, fmt.Errorf("内部错误， n2 %v > packSize %v - n %v", n2, packSize, n)
	}

	packData := buf[n+n2 : n+n2+int(packHeadSize)-n2]

	additionalData := buf[n+int(packHeadSize):]

	return PackType(packType), buf, packData, additionalData, nil
}

// 读取并解析包
// 返回值：
//	PackType	包类型
//	[]byte		包全部数据，即从 Mempool 申请的空间已使用的部分。内存使用完毕时，可以使用 mp.Put 释放回内存池
//	[]byte		序列化后的包数据
//	[]byte		附加数据
//	error
func ReadPack(r io.Reader, mp Mempool, pack Pack) (PackType, []byte, []byte, []byte, error) {
	if pack == nil {
		return 0, nil, nil, nil, fmt.Errorf("pack==nil")
	}

	packType, buf, packData, additonalData, err := ReadPackBuf(r, mp)
	if err != nil {
		return packType, buf, packData, additonalData, err
	}

	err = pack.Unmarshal(packData)
	if err != nil {
		return packType, buf, packData, additonalData, err
	}

	return packType, buf, packData, additonalData, nil
}

// 读取并解析包
// 返回值：
//  pack		包
//	PackType	包类型
//	[]byte		包全部数据，即从 Mempool 申请的空间已使用的部分。内存使用完毕时，可以使用 mp.Put 释放回内存池
//	[]byte		附加数据
//	error
func ReadPackAuto(r io.Reader, mp Mempool, auto func(packType PackType) (Pack, error)) (Pack, PackType, []byte, []byte, error) {
	if auto == nil {
		return nil, 0, nil, nil, fmt.Errorf("auto==nil")
	}

	packType, buf, packData, additonalData, err := ReadPackBuf(r, mp)
	if err != nil {
		return nil, packType, buf, additonalData, err
	}

	pack, err := auto(packType)
	if err != nil {
		return nil, packType, buf, additonalData, err
	}

	err = pack.Unmarshal(packData)
	if err != nil {
		return nil, packType, buf, additonalData, err
	}

	return pack, packType, buf, additonalData, nil
}

func PackAuto(packType PackType) (Pack, error) {

	// todo 使用对象池
	// 使用对象池时，别忘了使用前复位变量，否则可能部分位为上次使用者的值。

	switch packType {
	case PackTypeHello:
		return new(Hello), nil

	case PackTypeHelloR:
		return new(HelloR), nil

	case PackTypeStreamNew:
		return new(StreamNew), nil

	case PackTypeStreamData:
		return new(StreamData), nil

	case PackTypeStreamClose:
		return new(StreamClose), nil

	case PackTypeStreamRst:
		return new(StreamRst), nil

	case PackTypeStreamDown:
		return new(StreamDown), nil

	default:
		return nil, fmt.Errorf("unknown type %v", packType)
	}
}

func (p *Hello) GetPackType() PackType {
	return PackTypeHello
}
func (p *HelloR) GetPackType() PackType {
	return PackTypeHelloR
}
func (p *StreamNew) GetPackType() PackType {
	return PackTypeStreamNew
}
func (p *StreamData) GetPackType() PackType {
	return PackTypeStreamData
}
func (p *StreamClose) GetPackType() PackType {
	return PackTypeStreamClose
}
func (p *StreamRst) GetPackType() PackType {
	return PackTypeStreamRst
}

func (p *StreamDown) GetPackType() PackType {
	return PackTypeStreamDown
}

func (p *Hello) GetStreamId() uint64 {
	return 0
}
func (p *HelloR) GetStreamId() uint64 {
	return 0
}
func (p *StreamNew) GetStreamId() uint64 {
	return p.GetStreamId()
}
func (p *StreamData) GetStreamId() uint64 {
	return p.GetStreamId()
}
func (p *StreamClose) GetStreamId() uint64 {
	return p.GetStreamId()
}
func (p *StreamRst) GetStreamId() uint64 {
	return p.GetStreamId()
}
func (p *StreamDown) GetStreamId() uint64 {
	return p.GetStreamId()
}
