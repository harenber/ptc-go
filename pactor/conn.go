package pactor

import (
	"sync"
	"time"
)

type conn struct {
	*Modem
	closeOnce sync.Once
}

func (p *Modem) newConn(remotecall string) (*conn, error) {
	if err := p.call(remotecall); err != nil {
		return nil, err
	}
	return &conn{
		Modem: p,
	}, nil
}

func (c *conn) Close() error {
	writeDebug("conn.Close() called", 1)
	var err error
	err = c.disconnect()
	var timeout uint8
	for {
		if timeout > 30 {
			err = c.forceDisconnect()
			break
		}
		if c.channelState.f != 4 {
			break
		}
		timeout += 1
		time.Sleep(time.Second)
	}
	return err
}
