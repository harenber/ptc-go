package pactor

import (
	"net"

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

