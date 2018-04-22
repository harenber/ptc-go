package ptc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/la5nta/wl2k-go/transport"
)

func init() {
	transport.RegisterDialer("ptc", &pmodem{})
}

func (p *pmodem) DialURL(url *transport.URL) (net.Conn, error) {
	// url.Host holds the (optional) local address of the TNC (e.g. /dev/ttyUSB0)
	// url.Target holds the target node's callsign
	// url.User.Username() holds the callers (this node's) callsign
	// url.Params holds any additional parameters

	if url.Scheme != "ptc" {
		return nil, transport.ErrUnsupportedScheme
	}

	if str := url.Params.Get("host"); str != "" {
		p.deviceName = str
	} else {
		// assume we have a USB PTC at the first USB device if the user hasn't told us anything else
		// maybe we should have a cleverer guess here for non-Linux platforms.
		p.deviceName = "/dev/ttyUSB0"
	}

	if str := url.Params.Get("init_script"); str != "" {
		if _, err := os.Stat(str); os.IsNotExist(err) {
			return nil, errors.New(fmt.Sprintf("ERROR: PTC init script defined but not existent: %s", str))
		}
		p.init_script = str
	} else {
		p.init_script = ""
	}
	if str := url.Params.Get("baud"); str != "" {
		var err error
		p.baudRate, err = strconv.Atoi(str)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("ERROR: baud rate defined cannot be converted to an int: %s", str))
		}
	} else {
		p.baudRate = 57600
	}

	if _, err := os.Stat(p.deviceName); os.IsNotExist(err) {
		return nil, errors.New(fmt.Sprintf("ERROR: Device %s does not exist", p.deviceName))
	}
	p.mycall = url.User.Username()
	p.remotecall = url.Target
	err := p.init()
	if err != nil {
		return p, err
	}
	err = p.call()
	return p, err

	//	return Conn{
	//		fd:     strings.NewReader("The PTC driver\rBye bye!\r"),
	//		local:  Address{Callsign: url.User.Username()},
	//		remote: Address{Callsign: url.Target},
	//	}, nil
}

type Address struct{ Callsign string }

func (a Address) Network() string { return "" }
func (a Address) String() string  { return a.Callsign }
