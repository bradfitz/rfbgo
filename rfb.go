/*
Copyright 2011 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// toy VNC (RFB) server in Go, just learning the protocol.
//
// Protocol docs:
//    http://www.realvnc.com/docs/rfbproto.pdf
//
// Author: Brad Fitzpatrick <brad@danga.com>

package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"net"
	"os"
	"runtime/pprof"
	"strconv"
	"sync"
	"time"
)

var (
	profile = flag.Bool("profile", false, "write a cpu.prof file")
	listen  = flag.String("listen", ":5900", "listen on [ip]:port")
)

func main() {
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		conn := NewConn(c)
		go conn.serve()
	}
}

const (
	v3 = "RFB 003.003\n"
	v7 = "RFB 003.007\n"
	v8 = "RFB 003.008\n"

	authNone = 1

	statusOK     = 0
	statusFailed = 1

	encodingRaw      = 0
	encodingCopyRect = 1

	// Client -> Server
	cmdSetPixelFormat           = 0
	cmdSetEncodings             = 2
	cmdFramebufferUpdateRequest = 3
	cmdKeyEvent                 = 4
	cmdPointerEvent             = 5
	cmdClientCutText            = 6

	// Server -> Client
	cmdFramebufferUpdate = 0
)

// Fixed stuff, for now:
const (
	deskWidth  = 1280
	deskHeight = 720
)

type LockableImage struct {
	sync.Mutex
	Img image.Image
}

type Conn struct {
	c      net.Conn
	br     *bufio.Reader
	bw     *bufio.Writer
	fbupc  chan FrameBufferUpdateRequest
	closec chan bool // never sent; just closed

	// should only be mutated once during handshake, but then
	// only read.
	format PixelFormat

	feed chan *LockableImage
	mu   sync.RWMutex // guards last (but not its pixels, just the variable)
	last *LockableImage

	buf8 []uint8 // temporary buffer to avoid generating garbage
}

func NewConn(c net.Conn) *Conn {
	im := image.NewRGBA(image.Rect(0, 0, deskWidth, deskHeight))
	drawImage(im, 0)

	conn := &Conn{
		c:      c,
		br:     bufio.NewReader(c),
		bw:     bufio.NewWriter(c),
		fbupc:  make(chan FrameBufferUpdateRequest, 128),
		feed:   make(chan *LockableImage, 10),
		last:   &LockableImage{Img: im},
		closec: make(chan bool),
	}
	return conn
}

func (c *Conn) animateImage() {
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()
	for {
		select {
		case <-c.closec:
			return
		case <-tick.C:
			log.Printf("animate, slide=%d", slide)
			c.updateImageFrame()
		}
	}
}

var slide = 0

func (c *Conn) updateImageFrame() {
	c.last.Lock()
	slide++
	drawImage(c.last.Img.(*image.RGBA), slide)
	c.last.Unlock()
	c.feed <- c.last
}

func drawImage(im *image.RGBA, off int) {
	pos := 0
	for y := 0; y < deskHeight; y++ {
		for x := 0; x < deskWidth; x++ {
			c := color.RGBA{uint8(x), uint8(y), uint8(x + y + off), 0}
			switch {
			case x < (slide % 50):
				c = color.RGBA{R: 255}
			case x > deskWidth - 50:
				c = color.RGBA{G: 255}
			case y < 50-(slide%50):
				c = color.RGBA{R: 255, G: 255}
			case y > deskHeight - 50:
				c = color.RGBA{B: 255}
			}
			im.Pix[pos] = c.R
			im.Pix[pos+1] = c.G
			im.Pix[pos+2] = c.B
			pos += 4 // skipping alpha
		}
	}
}

func (c *Conn) dimensions() (w, h int) {
	return deskWidth, deskHeight
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
	defer close(c.fbupc)
	defer close(c.closec)
	defer func() {
		e := recover()
		if e != nil {
			log.Printf("Client disconnect: %v", e)
		}
	}()

	if *profile {
		f, err := os.Create("cpu.prof")
		if err != nil {
			log.Fatal(err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("profiling CPU")
		defer pprof.StopCPUProfile()
		defer log.Printf("stopping profiling CPU")
	}

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

	c.format = PixelFormat{
		BPP:        24,
		Depth:      24,
		BigEndian:  1,
		TrueColour: 1,
		RedMax:     255,
		GreenMax:   255,
		BlueMax:    255,
		RedShift:   16,
		GreenShift: 8,
		BlueShift:  0,
	}

	// 6.3.2. ServerInit
	// TODO: send what Screens requests? PixelFormat{BPP:0x10, Depth:0x10,
	// BigEndian:0x0, TrueColour:0x1, RedMax:0x1f, GreenMax:0x1f,
	// BlueMax:0x1f, RedShift:0xa, GreenShift:0x5, BlueShift:0x0}
	width, height := c.dimensions()
	c.w(uint16(width))
	c.w(uint16(height))
	c.w(c.format.BPP)
	c.w(c.format.Depth)
	c.w(c.format.BigEndian)
	c.w(c.format.TrueColour)
	c.w(c.format.RedMax)
	c.w(c.format.GreenMax)
	c.w(c.format.BlueMax)
	c.w(c.format.RedShift)
	c.w(c.format.GreenShift)
	c.w(c.format.BlueShift)
	c.w(uint8(0)) // pad1
	c.w(uint8(0)) // pad2
	c.w(uint8(0)) // pad3
	serverName := "rfb-go"
	c.w(int32(len(serverName)))
	c.bw.WriteString(serverName)
	c.flush()

	go c.pushFramesLoop()
	for {
		//log.Printf("awaiting command byte from client...")
		cmd := c.readByte("6.4:client-server-packet-type")
		//log.Printf("got command type %d from client", int(cmd))
		switch cmd {
		case cmdSetPixelFormat:
			c.handleSetPixelFormat()
		case cmdSetEncodings:
			c.handleSetEncodings()
		case cmdFramebufferUpdateRequest:
			c.handleUpdateRequest()
		case cmdPointerEvent:
			c.handlePointerEvent()
		case cmdKeyEvent:
			c.handleKeyEvent()
		default:
			c.failf("unsupported command type %d from client", int(cmd))
		}
	}
}

func (c *Conn) pushFramesLoop() {
	for {
		select {
		case ur, ok := <-c.fbupc:
			if !ok {
				// Client disconnected.
				return
			}
			c.pushFrame(ur)
		case li := <-c.feed:
			c.mu.Lock()
			c.last = li
			c.mu.Unlock()
			c.pushImage(li)
		}
	}
}

func (c *Conn) pushFrame(ur FrameBufferUpdateRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	li := c.last
	if li == nil {
		return
	}

	if ur.incremental() {
		li.Lock()
		defer li.Unlock()
		im := li.Img
		b := im.Bounds()
		width, height := b.Dx(), b.Dy()

		//log.Printf("Client wants incremental update, sending none. %#v", ur)
		c.w(uint8(cmdFramebufferUpdate))
		c.w(uint8(0))      // padding byte
		c.w(uint16(1))     // no rectangles
		c.w(uint16(0))     // x
		c.w(uint16(0))     // y
		c.w(uint16(width)) // x
		c.w(uint16(height))
		c.w(int32(encodingCopyRect))
		c.w(uint16(0)) // src-x
		c.w(uint16(0)) // src-y
		c.flush()
		return
	}
	c.pushImage(li)
}

func (c *Conn) pushImage(li *LockableImage) {
	li.Lock()
	defer li.Unlock()

	im := li.Img
	b := im.Bounds()
	if b.Min.X != 0 || b.Min.Y != 0 {
		panic("this code is lazy and assumes images with Min bounds at 0,0")
	}
	width, height := b.Dx(), b.Dy()

	c.w(uint8(cmdFramebufferUpdate))
	c.w(uint8(0))  // padding byte
	c.w(uint16(1)) // 1 rectangle

	//log.Printf("sending %d x %d pixels", width, height)

	if c.format.TrueColour == 0 {
		c.failf("only true-colour supported")
	}

	// Send that rectangle:
	c.w(uint16(0))     // x
	c.w(uint16(0))     // y
	c.w(uint16(width)) // x
	c.w(uint16(height))
	c.w(int32(encodingRaw))

	rgba, isRGBA := im.(*image.RGBA)
	if isRGBA && c.format.isScreensThousands() {
		// Fast path.
		c.pushRGBAScreensThousandsLocked(rgba)
	} else {
		c.pushGenericLocked(im)
	}
	c.flush()
}

func (c *Conn) pushRGBAScreensThousandsLocked(im *image.RGBA) {
	var u16 uint16
	pixels := len(im.Pix) / 4
	if len(c.buf8) < pixels*2 {
		c.buf8 = make([]byte, pixels*2)
	}
	out := c.buf8[:]
	isBigEndian := c.format.BigEndian != 0
	for i, v8 := range im.Pix {
		switch i % 4 {
		case 0: // red
			u16 = uint16(v8&248) << 7 // 3 masked bits + 7 shifted == redshift of 10
		case 1: // green
			u16 |= uint16(v8&248) << 2 // redshift of 5
		case 2: // blue
			u16 |= uint16(v8 >> 3)
		case 3: // alpha, unused.  use this to just move the dest
			hb, lb := uint8(u16>>8), uint8(u16)
			if isBigEndian {
				out[0] = hb
				out[1] = lb
			} else {
				out[0] = lb
				out[1] = hb
			}
			out = out[2:]
		}
	}
	c.bw.Write(c.buf8[:pixels*2])
}

// pushGenericLocked is the slow path generic implementation that works on
// any image.Image concrete type and any client-requested pixel format.
// If you're lucky, you never end in this path.
func (c *Conn) pushGenericLocked(im image.Image) {
	b := im.Bounds()
	width, height := b.Dx(), b.Dy()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			col := im.At(x, y)
			r16, g16, b16, _ := col.RGBA()
			r16 = inRange(r16, c.format.RedMax)
			g16 = inRange(g16, c.format.GreenMax)
			b16 = inRange(b16, c.format.BlueMax)
			var u32 uint32 = (r16 << c.format.RedShift) |
				(g16 << c.format.GreenShift) |
				(b16 << c.format.BlueShift)
			var v interface{}
			switch c.format.BPP {
			case 32:
				v = u32
			case 16:
				v = uint16(u32)
			case 8:
				v = uint8(u32)
			default:
				c.failf("TODO: BPP of %d", c.format.BPP)
			}
			if c.format.BigEndian != 0 {
				binary.Write(c.bw, binary.BigEndian, v)
			} else {
				binary.Write(c.bw, binary.LittleEndian, v)
			}
		}
	}
}

type PixelFormat struct {
	BPP, Depth                      uint8
	BigEndian, TrueColour           uint8 // flags; 0 or non-zero
	RedMax, GreenMax, BlueMax       uint16
	RedShift, GreenShift, BlueShift uint8
}

// Is the format requested by the OS X "Screens" app's "Thousands" mode.
func (f *PixelFormat) isScreensThousands() bool {
	return f.BPP == 16 && f.Depth == 16 && f.TrueColour != 0 &&
		f.RedMax == 0x1f && f.GreenMax == 0x1f && f.BlueMax == 0x1f &&
		f.RedShift == 10 && f.GreenShift == 5 && f.BlueShift == 0
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
	c.format = pf

	// TODO: not the right place to start this, but works for now.
	// We just want to make sure that the client has sent their preference
	// first.
	go c.animateImage()
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

func (r *FrameBufferUpdateRequest) incremental() bool { return r.IncrementalFlag != 0 }

// 6.4.3
func (c *Conn) handleUpdateRequest() {
	var req FrameBufferUpdateRequest
	c.read("framebuffer-update.incremental", &req.IncrementalFlag)
	c.read("framebuffer-update.x", &req.X)
	c.read("framebuffer-update.y", &req.Y)
	c.read("framebuffer-update.width", &req.Width)
	c.read("framebuffer-update.height", &req.Height)
	c.fbupc <- req
}

// 6.4.4
type KeyEvent struct {
	DownFlag uint8
	Key      uint32
}

// 6.4.4
func (c *Conn) handleKeyEvent() {
	var req KeyEvent
	c.read("key-event.downflag", &req.DownFlag)
	c.readPadding("key-event.padding", 2)
	c.read("key-event.key", &req.Key)
	log.Printf("%#v", req)
}

// 6.4.5
type PointerEvent struct {
	ButtonMask uint8
	X, Y       uint16
}

// 6.4.5
func (c *Conn) handlePointerEvent() {
	var req PointerEvent
	c.read("pointer-event.mask", &req.ButtonMask)
	c.read("pointer-event.x", &req.X)
	c.read("pointer-event.y", &req.Y)
	log.Printf("%#v", req)
}

func inRange(v uint32, max uint16) uint32 {
	switch max {
	case 0x1f: // 5 bits
		return v >> (16 - 5)
	}
	panic("todo; max value = " + strconv.Itoa(int(max)))
}
