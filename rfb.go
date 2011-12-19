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
		go handle(c)
	}
}

const (
	v3           = "RFB 003.003\n"
	v7           = "RFB 003.007\n"
	v8           = "RFB 003.008\n"
	authNone     = 1
	statusOK     = 0
	statusFailed = 1
)

func handle(c net.Conn) {
	defer c.Close()
	defer func() {
		e := recover()
		if e != nil {
			log.Printf("Client disconnect: %v", e)
		}
	}()

	br := bufio.NewReader(c)
	bw := bufio.NewWriter(c)

	readByte := func(which string) byte {
		b, err := br.ReadByte()
		if err != nil {
			panic(fmt.Sprintf("Error reading client byte for %q: %v", which, err))
		}
		return b
	}
	w := func(v interface{}) {
		binary.Write(bw, binary.BigEndian, v)
	}

	bw.WriteString("RFB 003.008\n")
	bw.Flush()
	sl, err := br.ReadSlice('\n')
	if err != nil {
		log.Printf("reading client protocol version: %v", err)
		return
	}
	ver := string(sl)
	log.Printf("client wants: %q", ver)
	switch ver {
	case v3, v7, v8: // cool.
	default:
		panic(fmt.Sprintf("bogus client-requested security type %q", ver))
	}

	// Auth
	if ver >= v7 {
		// Just 1 auth type supported: 1 (no auth)
		bw.WriteString("\x01\x01")
		bw.Flush()
		wanted := readByte("6.1.2:client requested security-type")
		if wanted != authNone {
			log.Printf("client wanted auth type %d, not None", int(wanted))
			return
		}
	} else {
		// Old way. Just tell client we're doing no auth.
		w(uint32(authNone))
		bw.Flush()
	}

	if ver >= v8 {
		// 6.1.3. SecurityResult
		w(uint32(statusOK))
		bw.Flush()
	}

	log.Printf("reading client init")

	// ClientInit
	wantShared := readByte("shared-flag") != 0
	_ = wantShared

	// 6.3.2. ServerInit
	width, height := 1024, 768
	w(uint16(width))
	w(uint16(height))
	w(uint8(32))   // bits-per-pixel
	w(uint8(32))   // depth
	w(uint8(1))    // big-endian-flag
	w(uint8(1))    // true-colour-flag
	w(uint16(255)) // red-max
	w(uint16(255)) // green-max
	w(uint16(255)) // blue-max
	w(uint8(16))   // red-shift
	w(uint8(8))    // green-shift
	w(uint8(0))    // blue-shift
	w(uint8(0))    // pad1
	w(uint8(0))    // pad2
	w(uint8(0))    // pad3
	bw.Flush()

	serverName := "rfb-go"
	w(int32(len(serverName)))
	bw.WriteString(serverName)
	bw.Flush()

	log.Printf("TODO")
	select {}
}
