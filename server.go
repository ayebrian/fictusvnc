package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

func runVNCServerWithRotator(addr string, rotator *ImageRotator, serverName string, showIP bool) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Get first image for logging
	firstImg := rotator.GetImage()
	log.Printf("[%s] Serving %d images with rotation on %s", serverName, len(rotator.images), addr)
	log.Printf("[%s] First image: %dx%d", serverName, firstImg.w, firstImg.h)

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("[%s] Accept error: %v", serverName, err)
			time.Sleep(time.Second)
			continue
		}
		go serveWithRotator(c, rotator, serverName, showIP)
	}
}

func serveWithRotator(c net.Conn, rotator *ImageRotator, serverName string, showIP bool) {
	defer func() {
		c.Close()
		log.Printf("[%s] Client disconnected", serverName)
	}()

	// Get image from rotator for this connection
	f := rotator.GetImageForConnection()

	var clientFB *fb
	if showIP {
		clientIP := strings.Split(c.RemoteAddr().String(), ":")[0]
		clientFB = addIPOverlay(f, clientIP)
	} else {
		clientFB = f
	}
	log.Printf("[%s] Client connected from %s", serverName, c.RemoteAddr())

	c.SetReadDeadline(time.Now().Add(30 * time.Second))

	_, err := c.Write([]byte(rfbVersion))
	if err != nil {
		log.Printf("[%s] Failed to send version: %v", serverName, err)
		return
	}

	buf := make([]byte, 12)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read client version: %v", serverName, err)
		return
	}

	_, err = c.Write([]byte{1, 1})
	if err != nil {
		log.Printf("[%s] Failed to send auth: %v", serverName, err)
		return
	}

	buf = make([]byte, 1)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read auth selection: %v", serverName, err)
		return
	}

	_, err = c.Write(make([]byte, 4))
	if err != nil {
		log.Printf("[%s] Failed to send auth result: %v", serverName, err)
		return
	}

	buf = make([]byte, 1)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		log.Printf("[%s] Failed to read client init: %v", serverName, err)
		return
	}

	pf := pixelFormat{32, 24, 0, 1, 255, 255, 255, 16, 8, 0, [3]byte{}}
	err = sendServerInit(c, clientFB.w, clientFB.h, pf, serverName)
	if err != nil {
		log.Printf("[%s] Failed to send server init: %v", serverName, err)
		return
	}

	var lastRejectedFormat string

	for {
		err := c.SetReadDeadline(time.Now().Add(30 * time.Second))
		if err != nil {
			log.Printf("[%s] Failed to set read deadline: %v", serverName, err)
			return
		}

		msgType, err := read1(c)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				log.Printf("[%s] Read timeout, closing connection", serverName)
			} else if err != io.EOF {
				log.Printf("[%s] Read error: %v", serverName, err)
			}
			return
		}

		switch msgType {
		case msgSetPixelFormat:
			_, err := readN(c, 3)
			if err != nil {
				log.Printf("[%s] Failed to read padding: %v", serverName, err)
				return
			}

			var buf [16]byte
			_, err = io.ReadFull(c, buf[:])
			if err != nil {
				log.Printf("[%s] Failed to read pixel format: %v", serverName, err)
				return
			}

			var want pixelFormat
			binary.Read(bytes.NewReader(buf[:]), binary.BigEndian, &want)
			formatID := fmt.Sprintf("%dbpp trueColor=%d", want.BPP, want.TrueColor)
			if want.TrueColor == 1 && (want.BPP == 32 || want.BPP == 24 || want.BPP == 8) {
				pf = want
				log.Printf("[%s] Client pixel format: %s", serverName, formatID)
				lastRejectedFormat = ""
			} else {
				if formatID != lastRejectedFormat {
					log.Printf("[%s] Unsupported format: %s — ignoring", serverName, formatID)
					lastRejectedFormat = formatID
				}
			}
		case msgSetEncodings:
			_, err := readN(c, 1)
			if err != nil {
				return
			}

			n, err := read16(c)
			if err != nil {
				return
			}

			_, err = readN(c, int(n)*4)
			if err != nil {
				return
			}
		case msgEnableCU:
			_, err := readN(c, 9)
			if err != nil {
				return
			}
		case msgFramebufferUpdateReq:
			_, err := readN(c, 9)
			if err != nil {
				return
			}

			err = sendFramebuffer(c, clientFB, pf)
			if err != nil {
				log.Printf("[%s] Failed to send framebuffer: %v", serverName, err)
				return
			}
		default:
			_, err := readN(c, 255)
			if err != nil {
				return
			}
		}
	}
}
