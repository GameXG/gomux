package protocol

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestUvarintSize(t *testing.T) {
	data := []uint64{0, 1, 2, 3, 0xF0, 0xFe, 0xFF, 0x100, 0xFFFe, 0xFFFF, 0x10000, 0xFFFFFFFFFFFFFFFD, 0xFFFFFFFFFFFFFFFE, 0xFFFFFFFFFFFFFFFF}

	for _, v := range data {
		testUvarintSize(t, v)
	}

	for sTime := time.Now(); time.Now().Sub(sTime) < 1*time.Second; {
		v := rand.Uint64()
		testUvarintSize(t, v)
	}
}

func testUvarintSize(t *testing.T, v uint64) {
	buf := make([]byte, binary.MaxVarintLen64)
	s := UvarintSize(v)
	n := binary.PutUvarint(buf, v)

	if s != n {
		t.Errorf("[v:%v] %v!=%v", v, s, n)
	}

	v2, n := binary.Uvarint(buf)
	if v != v2 {
		t.Errorf("%v!=%v", v, v2)
	}

	if s != n {
		t.Errorf("[v:%v] %v!=%v", v, s, n)
	}
}

func TestPackAuto(t *testing.T) {
	for id, name := range PackType_name {

		if id == 0 {
			continue
		}

		pack, err := PackAuto(PackType(id))
		if err != nil {
			t.Error(err)
			continue
		}

		if pack == nil {
			t.Error(name)
			continue
		}

		if packType := pack.GetPackType(); packType != PackType(id) {
			t.Error(packType)
		}
	}
}

func TestWritePack(t *testing.T) {
	type TestData struct {
		hello          *Hello
		additionalData []byte
	}

	testDataList := make([]*TestData, 0, 10)

	testDataList = append(testDataList, &TestData{
		hello:          &Hello{},
		additionalData: nil,
	})

	testDataList = append(testDataList, &TestData{
		hello: &Hello{
			ProtocolVersion: 123,
			LibraryVersion:  456,
			Feature:         nil,
		},
		additionalData: []byte("data112233445566"),
	})

	testDataList = append(testDataList, &TestData{
		hello:          &Hello{},
		additionalData: []byte("data112233445566"),
	})

	testDataList = append(testDataList, &TestData{
		hello: &Hello{
			ProtocolVersion: 123,
			LibraryVersion:  456,
			Feature:         nil,
		},
		additionalData: nil,
	})

	for _, v := range testDataList {
		testWritePack(t, v.hello, v.additionalData)
	}
}

func testWritePack(t *testing.T, hello *Hello, additionalData []byte) {
	buf := bytes.Buffer{}

	err := WritePack(&buf, hello, additionalData)
	if err != nil {
		t.Fatal(err)
	}

	pack, packType, _, additionalData2, err := ReadPackAuto(&buf, nil, PackAuto)
	if err != nil {
		t.Fatal(err)
	}

	hello2, ok := pack.(*Hello)
	if !ok {
		t.Error(ok)
	}

	if reflect.DeepEqual(hello, hello2) == false {
		t.Fatal(hello2)
	}

	if packType != PackTypeHello {
		t.Fatal(packType)
	}

	if bytes.Equal(additionalData, additionalData2) == false {
		t.Fatal(additionalData2)
	}
}
