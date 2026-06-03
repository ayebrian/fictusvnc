package main

import (
	"encoding/binary"
	"io"
	"net"
)

type pixelFormat struct {
	BPP, Depth             uint8
	BigEndian, TrueColor   uint8
	RMax, GMax, BMax       uint16
	RShift, GShift, BShift uint8
	_                      [3]byte // padding
}

func write16(c net.Conn, v ...uint16) error {
	for _, x := range v {
		err := binary.Write(c, binary.BigEndian, x)
		if err != nil {
			return err
		}
	}
	return nil
}

func write32(c net.Conn, v uint32) error {
	return binary.Write(c, binary.BigEndian, v)
}

func readN(c net.Conn, n int) (int64, error) {
	return io.CopyN(io.Discard, c, int64(n))
}

func read1(c net.Conn) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(c, b[:])
	return b[0], err
}

func read16(c net.Conn) (uint16, error) {
	var v uint16
	err := binary.Read(c, binary.BigEndian, &v)
	return v, err
}

func read32(c net.Conn) (uint32, error) {
	var v uint32
	err := binary.Read(c, binary.BigEndian, &v)
	return v, err
}

func sendServerInit(c net.Conn, w, h int, pf pixelFormat, name string) error {
	err := write16(c, uint16(w), uint16(h))
	if err != nil {
		return err
	}

	err = binary.Write(c, binary.BigEndian, pf)
	if err != nil {
		return err
	}

	err = write32(c, uint32(len(name)))
	if err != nil {
		return err
	}

	_, err = c.Write([]byte(name))
	return err
}
