package pactor

import (
	"os"
	"strconv"
	"log"
	"sync"
)

// DefaultDevice: assume we have a USB PTC at the first USB device if the user
//                hasn't told us anything else maybe we should have a cleverer
//                guess here for non-Linux platforms.
// DefaultBaud:
// SerialTimeout:
// PactorChannel:
// MaxSendData:
// MaxFrameNotTX:
const (
    DefaultBaud	   = 57600
	SerialTimeout  = 1
	PactorChannel  = 31
	MaxSendData    = 256
	MaxFrameNotTX  = 2
)

// Pactor states
const (
	Unknown      State = iota
	LinkSetup
	Connected
	DisconnectReq
	Disconnected
)

type State uint8

var debugMux sync.Mutex

func debugEnabled() int {
	if value, ok := os.LookupEnv("pactor_debug"); ok {
		level, err := strconv.Atoi(value)
		if err == nil {
			return level
		}
	}
	return 0
}

func writeDebug(message string, level int) {
	debugMux.Lock()
	defer debugMux.Unlock()
	if debugEnabled() >= level {
		log.Println(message)
	}
	return
}
