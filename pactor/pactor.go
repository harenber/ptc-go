package pactor

import (
	"log"
	"os"
	"runtime"
	"strconv"
	"sync"
)

// SerialTimeout: Timeout for read operations on serial bus
// PactorChannel: Pactor channel, 31 should work for both, PTC-IIex and P4 Dragon
// MaxSendData:   Pactor internal command sendbuf is 256 byte
// MaxFrameNotTX: Max. number of frames not transmitted at time.
const (
	SerialTimeout = 1
	PactorChannel = 4
	MaxSendData   = 255
	MaxFrameNotTX = 2
)

// Pactor states
const (
	Closed State = iota
	Closing
	ToBeClosed
	Ready
)

type State uint8

var debugMux sync.Mutex

func debugEnabled() int {
	if value, ok := os.LookupEnv("PACTOR_DEBUG"); ok {
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
		_, file, no, ok := runtime.Caller(1)
		if ok {
			log.Println(file + "#" + strconv.Itoa(no) + ": " + message)
		} else {
			log.Println(message)
		}
	}
	return
}
