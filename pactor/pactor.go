package pactor

import (
	"log"
	"os"
	"strconv"
	"sync"
)

// SerialTimeout: Timeout for read operations on serial bus
// PactorChannel: Pactor channel, 31 should work for both, PTC-IIex and P4 Dragon
// MaxSendData:   Pactor internal command buffer is 256 byte
// MaxFrameNotTX: Max. number of frames not transmitted at time.
const (
	SerialTimeout = 1
	PactorChannel = 31
	MaxSendData   = 256
	MaxFrameNotTX = 2
)

// Pactor states
const (
	Unknown State = iota
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
