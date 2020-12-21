package gomux

import (
	"net"
	"testing"
)

func TestSession_Handshake(t *testing.T) {
	conf := Config{}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			go func() {


				server := Server(c, &conf)

				server.
			}()
		}
	}()

}
