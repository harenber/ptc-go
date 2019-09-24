package pactor

import (
	"fmt"
	"net"
	"strconv"
	"io"
	"time"
	"runtime"

	"github.com/la5nta/wl2k-go/transport"
)

// DialURL dials pactor:// URLs
//
// BLOCK until connection is established or timeoud occured
func (p *Modem) DialURL(url *transport.URL) (net.Conn, error) {
	if url.Scheme != "pactor" {
		return nil, transport.ErrUnsupportedScheme
	}

	if err := p.call(url.Target); err != nil {
		return nil, err
	}

	return p, nil
}

// Read bytes from the Software receive buffer. The receive thread takes care of
// reading them from the pactor modem
//
// BLOCK until receive buffer has any data!
func (p *Modem) Read(d []byte) (int, error) {
	p.mux.read.Lock()
	defer p.mux.read.Unlock()

	if p.state != Connected  {
		return 0, fmt.Errorf("Read from closed connection")
	}

	if len(d) == 0 {
		return 0, nil
	}

	data, ok := <-p.recvBuf
	if !ok {
		return 0, io.EOF
	}

	if len(data) > len(d) {
		panic("too large") //TODO: Handle
	}

	for i, b := range data {
		d[i] = byte(b)
	}

	return len(data), nil
}

// Write bytes to the software send buffer. The send thread will take care of
// fowarding them to the pactor modem
//
// BLOCK if send buffer is full! Remains as soon as there is space left in the
// send buffer
func (p *Modem) Write(d []byte) (int, error) {
	p.mux.write.Lock()
	defer p.mux.write.Unlock()

	if p.state != Connected  {
		return 0, fmt.Errorf("Read from closed connection")
	}

	for _, b := range d {
		select {
		case <- p.flags.closeWriting:
			return 0, fmt.Errorf("Writing on closed connection")
		case p.sendBuf <- b:
		}

		p.mux.bufLen.Lock()
		p.sendBufLen++
		p.mux.bufLen.Unlock()
	}

	return len(d), nil
}

// Flush waits for the last frames to be transmitted.
//
// Will throw error if remaining frames could not bet sent within 120s
func (p *Modem) Flush() (err error) {
	if p.state != Connected  {
		return fmt.Errorf("Flush a closed connection")
	}

	writeDebug("Flush called", 2)
	if err = p.waitTransmissionFinish(120 * time.Second); err != nil {
		writeDebug(err.Error(), 2)
	}
	return
}

// Close closes the current connection.
//
// Will abort ("dirty disconnect") after 60 seconds if normal "disconnect" have
// not succeeded yet.
func (p *Modem) Close() error {
	p.mux.close.Lock()

	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("Close called from " + file + "#" + strconv.Itoa(no), 2)
	} else {
		writeDebug("Close called", 2)
	}

	if p.flags.closeCalled != true {
		defer p.close()
		defer p.mux.close.Unlock()

		p.flags.closeCalled = true

		if err := p.waitTransmissionFinish(90 * time.Second); err != nil {
			writeDebug(err.Error(), 2)
		}

		p.disconnect()

		if err := p.waitTransmissionFinish(30 * time.Second); err != nil {
			writeDebug(err.Error(), 2)
		}

		select {
		case <-p.flags.disconnected:
			writeDebug("Disconnect successful", 1)
			return nil
		case <-time.After(60 * time.Second):
			p.forceDisconnect()
			return fmt.Errorf("Disconnect timed out")
		}

		runtime.SetFinalizer(p, nil)
	}

	writeDebug("Should never reach this...", 1)
	return nil
}

// TxBufferLen returns the number of bytes in the out buffer queue.
//
func (p *Modem) TxBufferLen() int {
	p.mux.bufLen.Lock()
	defer p.mux.bufLen.Unlock()
	writeDebug("TxBufferLen called (" + strconv.Itoa(p.sendBufLen) + " bytes remaining in buffer)", 2)
	return p.sendBufLen + (p.getNumFramesNotTransmitted() * MaxSendData)
}
