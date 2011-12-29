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

// Example of using the rfb package.
//
// Author: Brad Fitzpatrick <brad@danga.com>

package main

import (
	"flag"
	"image"
	"image/color"
	"log"
	"net"
	"os"
	"runtime/pprof"
	"time"

	"github.com/bradfitz/rfbgo/rfb"
)

var (
	listen  = flag.String("listen", ":5900", "listen on [ip]:port")
	profile = flag.Bool("profile", false, "write a cpu.prof file when client disconnects")
)

const (
	width  = 1280
	height = 720
)

func main() {
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}

	s := rfb.NewServer(width, height)
	go func() {
		err = s.Serve(ln)
		log.Fatalf("rfb server ended with: %v", err)
	}()
	for c := range s.Conns {
		handleConn(c)
	}
}

func handleConn(c *rfb.Conn) {
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

	im := image.NewRGBA(image.Rect(0, 0, width, height))
	li := &rfb.LockableImage{Img: im}

	closec := make(chan bool)
	go func() {
		slide := 0
		tick := time.NewTicker(time.Second / 30)
		defer tick.Stop()
		for {
			select {
			case <-closec:
				return
			case <-tick.C:
				slide++
				li.Lock()
				drawImage(im, slide)
				li.Unlock()
				c.Feed <- li
			}
		}
	}()

	for e := range c.Event {
		log.Printf("got event: %#v", e)
	}
	close(closec)
	log.Printf("Client disconnected")
}

func drawImage(im *image.RGBA, anim int) {
	pos := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := color.RGBA{uint8(x), uint8(y), uint8(x + y + anim), 0}
			switch {
			case x < (anim % 50):
				c = color.RGBA{R: 255}
			case x > width-50:
				c = color.RGBA{G: 255}
			case y < 50-(anim%50):
				c = color.RGBA{R: 255, G: 255}
			case y > height-50:
				c = color.RGBA{B: 255}
			}
			im.Pix[pos] = c.R
			im.Pix[pos+1] = c.G
			im.Pix[pos+2] = c.B
			pos += 4 // skipping alpha
		}
	}
}
