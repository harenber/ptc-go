package pactor

import (
	"errors"
	"net"
	"sync"
)

// type listener struct {
// 	incoming <-chan net.Conn
// 	quit     chan struct{}
// 	errors   <-chan error
// 	addr     Addr
// }

// func (l listener) Accept() (c net.Conn, err error) {
// 	select {
// 	case c, ok := <-l.incoming:
// 		if !ok {
// 			return nil, io.EOF
// 		}
// 		return c, nil
// 	case err = <-l.errors:
// 		return nil, err
// 	}
// }

// func (l listener) Addr() net.Addr {
// 	return l.addr
// }

// func (l listener) Close() error {
// 	close(l.quit)
// 	return nil
// }

type listener struct {
	*Modem
	done      chan struct{}
	closeOnce sync.Once
}

func (p *Modem) Listen() (ln net.Listener, err error) {

	//return nil, errors.New("Pactor driver currently not supporting listening")
	l := &listener{Modem: p, done: make(chan struct{})}
	l.flags.listenMode = true
	return l, nil
}

// Accept: when in listening mode, BLOCK until a connection is received.
func (li *listener) Accept() (net.Conn, error) {
	/*	for li.state == Closing {
			time.Sleep(time.Second)
		}
		if li.state == Closed {
			err := li.init()
			if err != nil {
				return nil, err
			}
		}
	*/
	writeDebug("Entering listener Accept method", 1)

	select {
	case <-li.done:
		return nil, errors.New("Listener already closed")
	case <-li.flags.connected:
		writeDebug("incoming connection received", 1)
		return li, nil
	}
}

// Close closes the listener
func (li *listener) Close() error {
	writeDebug("listen.Close() called", 1)
	li.closeOnce.Do(func() {
		li.disconnect()
		close(li.done)
	})
	writeDebug("listen.Close() finished", 1)
	return nil
}
