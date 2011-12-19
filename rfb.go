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
	v3                          = "RFB 003.003\n"
	v7                          = "RFB 003.007\n"
	v8                          = "RFB 003.008\n"
	authNone                    = 1
	statusOK                    = 0
	statusFailed                = 1
	cmdSetPixelFormat           = 0
	cmdSetEncodings             = 2
	cmdFramebufferUpdateRequest = 3
	cmdKeyEvent                 = 4
	cmdPointerEvent             = 5
	cmdClientCutText            = 6
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

func (c *Conn) readPadding(what string, size int) {
	for i := 0; i < size; i++ {
		c.readByte(what)
	}
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
	// TODO: send what Screens requests? PixelFormat{BPP:0x10, Depth:0x10,
	// BigEndian:0x0, TrueColour:0x1, RedMax:0x1f, GreenMax:0x1f,
	// BlueMax:0x1f, RedShift:0xa, GreenShift:0x5, BlueShift:0x0}
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
		log.Printf("awaiting command byte from client...")
		cmd := c.readByte("6.4:client-server-packet-type")
		log.Printf("got command type %d from client", int(cmd))
		switch cmd {
		case cmdSetPixelFormat:
			c.handleSetPixelFormat()
		case cmdSetEncodings:
			c.handleSetEncodings()
		case cmdFramebufferUpdateRequest:
			c.handleUpdateRequest()
		default:
			c.failf("unsupported command type %d from client", int(cmd))
		}
	}
}

type PixelFormat struct {
	BPP, Depth                      uint8
	BigEndian, TrueColour           uint8 // flags; 0 or non-zero
	RedMax, GreenMax, BlueMax       uint16
	RedShift, GreenShift, BlueShift uint8
}

// 6.4.1
func (c *Conn) handleSetPixelFormat() {
	log.Printf("handling setpixel format")
	c.readPadding("SetPixelFormat padding", 3)
	var pf PixelFormat
	c.read("pixelformat.bpp", &pf.BPP)
	c.read("pixelformat.depth", &pf.Depth)
	c.read("pixelformat.beflag", &pf.BigEndian)
	c.read("pixelformat.truecolour", &pf.TrueColour)
	c.read("pixelformat.redmax", &pf.RedMax)
	c.read("pixelformat.greenmax", &pf.GreenMax)
	c.read("pixelformat.bluemax", &pf.BlueMax)
	c.read("pixelformat.redshift", &pf.RedShift)
	c.read("pixelformat.greenshift", &pf.GreenShift)
	c.read("pixelformat.blueshift", &pf.BlueShift)
	c.readPadding("SetPixelFormat pixel format padding", 3)
	log.Printf("Client wants pixel format: %#v", pf)
}

// 6.4.2
func (c *Conn) handleSetEncodings() {
	c.readPadding("SetEncodings padding", 1)

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

// 6.4.3
type FrameBufferUpdateRequest struct {
	IncrementalFlag     uint8
	X, Y, Width, Height uint16
}

// 6.4.3
func (c *Conn) handleUpdateRequest() {
	var req FrameBufferUpdateRequest
	c.read("framebuffer-update.incremental", &req.IncrementalFlag)
	c.read("framebuffer-update.x", &req.X)
	c.read("framebuffer-update.y", &req.Y)
	c.read("framebuffer-update.width", &req.Width)
	c.read("framebuffer-update.height", &req.Height)
	log.Printf("client requests update: %#v", req)
}
