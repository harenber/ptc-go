package pactor

import (
	"fmt"
	"net"
	"strconv"
	"io"
	"time"
	"sync"

	"github.com/la5nta/wl2k-go/transport"
)

type cmux struct {
	write  sync.Mutex
	read   sync.Mutex
	close  sync.Mutex
}

type Conn struct {
	m   *Modem
	mux cmux
}

func (c *Conn) RemoteAddr() net.Addr { return Addr{c.m.remoteAddr} }
func (c *Conn) LocalAddr() net.Addr  { return Addr{c.m.localAddr} }

func (c *Conn) SetDeadline(t time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(t time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }


// DialURL dials pactor:// URLs
//
// BLOCK until connection is established or timeoud occured
func (m *Modem) DialURL(url *transport.URL) (net.Conn, error) {
	if url.Scheme != "pactor" {
		return nil, transport.ErrUnsupportedScheme
	}

	if err := m.call(url.Target); err != nil {
		return nil, err
	}

	return &Conn{ m, cmux{} }, nil
}

// Read bytes from the Software receive buffer. The receive thread takes care of
// reading them from the pactor modem
//
// BLOCK until receive buffer has any data!
func (c *Conn) Read(d []byte) (int, error) {
	c.mux.read.Lock()
	defer c.mux.read.Unlock()

	if c.m.state != Connected  {
		return 0, fmt.Errorf("Read from closed connection")
	}

	if len(d) == 0 {
		return 0, nil
	}

	data, ok := <-c.m.recvBuf
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
func (c *Conn) Write(d []byte) (int, error) {
	c.mux.write.Lock()
	defer c.mux.write.Unlock()

	if c.m.state != Connected  {
		return 0, fmt.Errorf("Read from closed connection")
	}

	for _, b := range d {
		select {
		case <- c.m.flags.closeWriting:
			return 0, fmt.Errorf("Writing on closed connection")
		case c.m.sendBuf <- b:
		}

		c.m.mux.bufLen.Lock()
		c.m.sendBufLen++
		c.m.mux.bufLen.Unlock()
	}

	return len(d), nil
}

// Flush waits for the last frames to be transmitted.
//
// Will throw error if remaining frames could not bet sent within 120s
func (c *Conn) Flush() (err error) {
	if c.m.state != Connected  {
		return fmt.Errorf("Flush a closed connection")
	}

	writeDebug("Flush called", 2)
	if err = c.m.waitTransmissionFinish(120 * time.Second); err != nil {
		writeDebug(err.Error(), 2)
	}
	return
}

// Close closes the current connection.
//
// Will abort ("dirty disconnect") after 60 seconds if normal "disconnect" has
// not succeeded yet.
func (c *Conn) Close() error {
	if c.m.flags.closed {
		return nil
	}

	c.mux.close.Lock()
	defer c.mux.close.Unlock()

	return c.m.Close()
}

// TxBufferLen returns the number of bytes in the out buffer queue.
//
func (c *Conn) TxBufferLen() int {
	c.m.mux.bufLen.Lock()
	defer c.m.mux.bufLen.Unlock()
	writeDebug("TxBufferLen called (" + strconv.Itoa(c.m.sendBufLen) + " bytes remaining in buffer)", 2)
	return c.m.sendBufLen + (c.m.getNumFramesNotTransmitted() * MaxSendData)
}
