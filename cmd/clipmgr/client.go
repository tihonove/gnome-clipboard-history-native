//go:build linux

// client.go — «звонок» демону: clipmgr --show (его дёргает GNOME-хоткей).
package main

import (
	"log"
	"net"
)

func runClient() {
	c, err := net.Dial("unix", sockPath())
	if err != nil {
		log.Fatalf("демон не запущен (%v)", err)
	}
	defer c.Close()
	c.Write([]byte("show\n"))
}
