package pactor

import (
	"io"
	"net"
	"errors"
)

type listener struct {
	incoming <-chan net.Conn
	quit     chan struct{}
	errors   <-chan error
	addr     Addr
}

func (l listener) Accept() (c net.Conn, err error) {
	select {
	case c, ok := <-l.incoming:
		if !ok {
			return nil, io.EOF
		}
		return c, nil
	case err = <-l.errors:
		return nil, err
	}
}

func (l listener) Addr() net.Addr {
	return l.addr
}

func (l listener) Close() error {
	close(l.quit)
	return nil
}

func (p *Modem) Listen() (ln net.Listener, err error) {
	return nil, errors.New("Pactor driver currently not supporting listening")
}
