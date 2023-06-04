package pactor

import (
	"context"
	"net"

	"github.com/la5nta/wl2k-go/transport"
)

// DialURL dials pactor:// URLs
//
// BLOCK until connection is established or timeoud occured
func (p *Modem) DialURLContext(ctx context.Context, url *transport.URL) (conn net.Conn, err error) {
	if url.Scheme != "pactor" {
		return nil, transport.ErrUnsupportedScheme
	}
	var done = make(chan struct{})
	go func() {
		conn, err = p.DialURL(url)
		close(done)
	}()
	select {
	case <-done:
		return p, err
	case <-ctx.Done():
		p.forceDisconnect()
		p.Close()
		return nil, ctx.Err()
	}
}

func (p *Modem) DialURL(url *transport.URL) (net.Conn, error) {
	if url.Scheme != "pactor" {
		return nil, transport.ErrUnsupportedScheme
	}
	return p.newConn(url.Target)

}
