package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"log"
	"net"
)

func main() {
	ln, err := net.Listen("tcp", ":5901")
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		conn := &Conn{
			c:  c,
			br: bufio.NewReader(c),
			bw: bufio.NewWriter(c),
		}
		go conn.serve()
	}
}

const (
	v3              = "RFB 003.003\n"
	v7              = "RFB 003.007\n"
	v8              = "RFB 003.008\n"
	authNone        = 1
	statusOK        = 0
	statusFailed    = 1
	cmdSetEncodings = 2
)

type Conn struct {
	c  net.Conn
	br *bufio.Reader
	bw *bufio.Writer
}

func (c *Conn) readByte(what string) byte {
	b, err := c.br.ReadByte()
	if err != nil {
		c.failf("reading client byte for %q: %v", what, err)
	}
	return b
}

func (c *Conn) read(what string, v interface{}) {
	err := binary.Read(c.br, binary.BigEndian, v)
	if err != nil {
		c.failf("reading from client into %T for %q: %v", v, what, err)
	}
}

func (c *Conn) w(v interface{}) {
	binary.Write(c.bw, binary.BigEndian, v)
}

func (c *Conn) flush() {
	c.bw.Flush()
}

func (c *Conn) failf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func (c *Conn) serve() {
	defer c.c.Close()
	defer func() {
		e := recover()
		if e != nil {
			log.Printf("Client disconnect: %v", e)
		}
	}()

	c.bw.WriteString("RFB 003.008\n")
	c.flush()
	sl, err := c.br.ReadSlice('\n')
	if err != nil {
		c.failf("reading client protocol version: %v", err)
	}
	ver := string(sl)
	log.Printf("client wants: %q", ver)
	switch ver {
	case v3, v7, v8: // cool.
	default:
		c.failf("bogus client-requested security type %q", ver)
	}

	// Auth
	if ver >= v7 {
		// Just 1 auth type supported: 1 (no auth)
		c.bw.WriteString("\x01\x01")
		c.flush()
		wanted := c.readByte("6.1.2:client requested security-type")
		if wanted != authNone {
			c.failf("client wanted auth type %d, not None", int(wanted))
		}
	} else {
		// Old way. Just tell client we're doing no auth.
		c.w(uint32(authNone))
		c.flush()
	}

	if ver >= v8 {
		// 6.1.3. SecurityResult
		c.w(uint32(statusOK))
		c.flush()
	}

	log.Printf("reading client init")

	// ClientInit
	wantShared := c.readByte("shared-flag") != 0
	_ = wantShared

	// 6.3.2. ServerInit
	width, height := 1024, 768
	c.w(uint16(width))
	c.w(uint16(height))
	c.w(uint8(32))   // bits-per-pixel
	c.w(uint8(32))   // depth
	c.w(uint8(1))    // big-endian-flag
	c.w(uint8(1))    // true-colour-flag
	c.w(uint16(255)) // red-max
	c.w(uint16(255)) // green-max
	c.w(uint16(255)) // blue-max
	c.w(uint8(16))   // red-shift
	c.w(uint8(8))    // green-shift
	c.w(uint8(0))    // blue-shift
	c.w(uint8(0))    // pad1
	c.w(uint8(0))    // pad2
	c.w(uint8(0))    // pad3
	serverName := "rfb-go"
	c.w(int32(len(serverName)))
	c.bw.WriteString(serverName)
	c.flush()

	for {
		cmd := c.readByte("6.4:client-server-packet-type")
		log.Printf("got command type %d from client", int(cmd))
		switch cmd {
		case cmdSetEncodings:
			c.handleSetEncodings()
		default:
			c.failf("unsupported command type %d from client", int(cmd))
		}
	}
}

func (c *Conn) handleSetEncodings() {
	c.readByte("6.4.2:padding")
	var numEncodings uint16
	c.read("6.4.2:number-of-encodings", &numEncodings)
	var encType []int32
	for i := 0; i < int(numEncodings); i++ {
		var t int32
		c.read("encoding-type", &t)
		encType = append(encType, t)
	}
	log.Printf("Client encodings: %#v", encType)
}
