//go:build linux

// client.go — the "ring" to the daemon: gnome-clipboard-history-native --show (invoked by the GNOME hotkey).
package main

import (
	"log"
	"net"
)

func runClient() {
	c, err := net.Dial("unix", sockPath())
	if err != nil {
		log.Fatalf("daemon is not running (%v)", err)
	}
	defer c.Close()
	c.Write([]byte("show\n"))
}
