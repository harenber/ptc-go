// pmodem
package ptc

/*

TOOO list:

- implement a timer in the mainloop that breaks the connection if nothing happens
  during the pttimeout
- WA8DED resync (should not be happen nowadays, UARTs exist since decades)

*/
import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/tarm/serial"
)

type pmodem struct {
	deviceName string

	//ctrl net.Conn
	//data net.Conn

	mycall     string
	remotecall string

	//signal to disconnect PACTOR and close network
	closemu     sync.Mutex
	closecalled bool

	init_script string // file with commands to initialize the PTC
	baudRate    int

	rts chan struct{} //ready to send (= txbuffer is not full)
	rtd chan struct{} //ready to disconnect (=txbuffer empty)

	rxbuffer chan []byte // data FROM PACTOR
	device   *serial.Port

	mu       sync.Mutex
	txbuffer []byte // data TO PACTOR

	// if we have an error, we pass it back to Pat
	err error
}

const (
	pactorch      = 4
	serialtimeout = 1
	pttimeout     = 240
	maxtxbuffer   = 128 // Write() will block if there are more bytes waiting to be sent.
	//Note that the PTC has a large internal buffer and will fill that without tellng that the data is only in the buffer.
	//I haven't found a way to avoid that behaviour.
)

type reader interface {
	ReadString(delim byte) (line string, err error)
}

func debugEnabled() bool {
	return os.Getenv("ptc_debug") != ""
}

func writeDebug(message string) {
	if debugEnabled() {
		log.Println(message)
	}
	return
}

func (p *pmodem) HandleIOError(hint string, err error) {
	if err != nil {
		if p.closecalled == false {
			p.err = err //pass it back to Pat
			//log.Println("IOError while " + hint + ". Cannot continue, trying to close. Error is: " + err.Error())
			p.Close()
		} else {
			//EOF errors are normal when Close() has been called
			p.endwa8ded()
		}

	}
	return
}

func (p *pmodem) LocalAddr() net.Addr {
	h := Address{Callsign: p.mycall}
	return h
}

func (p *pmodem) RemoteAddr() net.Addr {
	h := Address{Callsign: p.remotecall}
	return h
}

func (p *pmodem) Close() error {
	// send a disconnect command to the PTC,
	p.rawwrite(pactorch, 1, "D")
	_, p.err = p.readbyte(2)

	p.closemu.Lock()
	if p.closecalled {
		// Close() was already called so disconnect is already in progress.
		// Avoid interfering by just return is Close() is called more than once
		p.closemu.Unlock()
		return p.err
	}

	p.closecalled = true
	p.closemu.Unlock()
	//new lock
	//waiting for message queue to be sent, but after one minute, we close and disconnect even if not all data has been sent
	select {
	case <-p.rtd:
	//
	case <-time.After(60 * time.Second):
		//
	}
	//old lock
	//	for len(p.txbuffer) != 0 {
	//		time.Sleep(200 * time.Millisecond)
	//	}
	p.endwa8ded()
	return p.err
}

func (p *pmodem) checkState() error {
	switch {
	case p.err != nil:
		return p.err
	case p.closecalled:
		return errors.New("Use of closed connection")
	default:
		return nil
	}
}

func (p *pmodem) Read(b []byte) (n int, err error) {
	if err := p.checkState(); err != nil {
		return 0, err
	}
	for len(p.rxbuffer) == 0 {
		time.Sleep(time.Second)
	}

	a := 0
	//p.mu.Lock() // (martinhpedersen) I do not think it's necessary to grab this lock here
	select {
	case msg := <-p.rxbuffer:
		a = len(msg)
		if len(b) < len(msg) {
			writeDebug("BUFFER IS TOO SMALL!!!!")
		}

		//log.Println("<<<PT<<< " + strconv.Itoa(len(msg)) + ": " + hex.EncodeToString(msg))
		//		for i, x := range msg {
		//			b[i] = x
		//		}
		copy(b, msg)
	case <-time.After(1 * time.Second):
		writeDebug("Reading from rxbuffer timed out")
	}

	return a, nil
}

func (p *pmodem) Write(b []byte) (n int, err error) {
	if err := p.checkState(); err != nil {
		return 0, err
	}
	<-p.rts
	p.mu.Lock()
	p.txbuffer = append(p.txbuffer, b...)
	n = len(p.txbuffer)
	p.mu.Unlock()
	//log.Println(">>>PT>>> " + string(b))

	return n, nil
}

func (p *pmodem) SetDeadline(t time.Time) error {
	// to be implmented
	return nil
}

func (p *pmodem) SetReadDeadline(t time.Time) error {
	// to be implmented
	return nil
}

func (p *pmodem) SetWriteDeadline(t time.Time) error {
	// to be implmented
	return nil
}

func inttobin(in []uint8) (b []byte) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, in)
	if err != nil {
		writeDebug("binary.Write failed:" + err.Error())
	}
	return buf.Bytes()
}

func read(r reader, delim []byte) (line []byte, err error) {
	for {
		s := ""
		s, err = r.ReadString(delim[len(delim)-1])

		if err != nil {
			return
		}

		line = append(line, []byte(s)...)
		if bytes.HasSuffix(line, delim) {
			return line[:len(line)-len(delim)], nil
		}
	}
}

func (p *pmodem) writeexpect(command string, answer string) (b []byte, err error) {
	_, err = p.device.Write([]byte(command + "\r"))
	//log.Println(">>> " + command)
	if err != nil {
		writeDebug(err.Error())
		return nil, err
	}

	b, err = p.readuntil(answer)
	//log.Printf("<<< %s\n", b)
	return
}

func (p *pmodem) startwa8ded() (err error) {
	_, err = p.writeexpect("JHOST1", "JHOST1")
	if err != nil {
		writeDebug("Couldn't go into WA8DED hostmode, no answer to the JHOST1 command")
	}
	return err
}

func (p *pmodem) endwa8ded() (err error) {
	// call disconnect here "just in case", that should not be needed, but
	// doesn't harm either.
	p.rawwrite(pactorch, 1, "D")
	_, err = p.readbyte(2)
	p.rawwrite(0, 1, "JHOST0")
	_, err = p.device.Write([]byte("\r\n"))
	return
}

func (p *pmodem) readuntil(answer string) (b []byte, err error) {
	// p.mu.Lock() (martinhpedersen) Don't think it's necessary to grab this lock here
	reader := bufio.NewReader(p.device)
	b, err = read(reader, []byte(answer))
	// p.mu.Unlock() (martinhpedersen) See above

	if err != nil {
		writeDebug("Error while reading to the answer " + hex.EncodeToString([]byte(answer)) + ":" + err.Error())
	}
	return b, err

}

func (p *pmodem) readbyte(noofbytes int) ([]byte, error) {
	buf := make([]byte, noofbytes)
	var err error

	if _, err := os.Stat(p.deviceName); os.IsNotExist(err) {
		// the serial device has gone, either it has been disconnected or
		// a bluetooth line was disturbed. So it makes no sense to try to
		// Read(), rather throw an error.
		return buf, errors.New(fmt.Sprintf("serial device %s vanished!", p.deviceName))
	}
	n, err := p.device.Read(buf)
	if err != nil {
		if err != io.EOF {
			writeDebug(err.Error())
		}
	}
	if n != noofbytes {
		// The serial device Read() ran into the timeout and hasn't returned
		// all bytes. Throw an error
		err = errors.New(fmt.Sprintf("timeout while reading %d bytes", noofbytes))
	}

	return buf, err
}

func (p *pmodem) rawwrite(channel uint8, iscommand uint8, command string) (err error) {
	var l uint8 = uint8(len(command)) - 1
	cmd := []byte(command)
	init := inttobin([]uint8{channel, iscommand, l})
	if l < 0 {
		// that should never happen!
		writeDebug("rawwrite: Something is wrong with command " + command + " len is: " + strconv.Itoa(int(l)))

		return errors.New("Command length is negative on rawwrite command")
	}

	if _, err = os.Stat(p.deviceName); os.IsNotExist(err) {
		// the serial device has gone, either it has been disconnected or
		// a bluetooth line was disturbed. So it makes no sense to try to
		// Read(), rather throw an error.
		return errors.New(fmt.Sprintf("serial device %s vanished!", p.deviceName))
	}
	_, err = p.device.Write([]byte(fmt.Sprintf("%s%s", init, cmd)))
	time.Sleep(100 * time.Millisecond)
	return

}

func split(buf []byte, lim int) [][]byte {
	var chunk []byte
	chunks := make([][]byte, 0, len(buf)/lim+1)
	for len(buf) >= lim {
		chunk, buf = buf[:lim], buf[lim:]
		chunks = append(chunks, chunk)
	}
	if len(buf) > 0 {
		chunks = append(chunks, buf[:len(buf)])
	}
	return chunks
}

func (p *pmodem) mainloop() {

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// function to handle SIGINT, SIGTERM.
	// at the moment it just tries to leave the WA8DED hostmode
	go func() {
		sig := <-sigs
		writeDebug(sig.String() + " mainloop: signal received, exiting")
		p.endwa8ded()
	}()

	//this is the main loop!
	for p.closecalled == false {
		// This would be the code to loop over all channels (not needed here)
		//		p.rawwrite(0xff, 1, "G")
		//		b, _ := p.readbyte(2)
		//		channels, _ := p.readuntil(string(byte(0x00)))
		//				for _, channel := range channels {

		var err error
		err = p.rawwrite(pactorch, 1, "G")

		if err != nil {
			p.HandleIOError("writing G-command", err)
			return
		}

		b, err := p.readbyte(2)
		if err != nil {
			p.HandleIOError("reading channel after G-command", err)
			return
		}

		switch b[1] {
		case byte(0x00):
		//nothing to do
		case byte(0x01):
			//success to command, null terminated message
			_, err = p.readuntil(string(byte(0)))
			//log.Println("1: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))
		case byte(0x02):
			//error to command, null terminated message
			_, err = p.readuntil(string(byte(0)))
			//log.Println("2: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))
		case byte(0x03):
			// link status, null terminated message
			_, err = p.readuntil(string(byte(0)))
			//log.Println("3: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))
		case byte(0x04):
			//monitor w/o data, null terminated message
			_, err = p.readuntil(string(byte(0)))
			//log.Println("4: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))
		case byte(0x05):
			//monitor with data, null terminated message
			_, err = p.readuntil(string(byte(0)))
			//log.Println("5: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))
		case byte(0x06):
			// Monitor data
			length, _ := p.readbyte(1)
			//lust discard monitor data for the time being
			_, err = p.readbyte(int(length[0]) + 1)
			//			data, _ := p.readbyte(int(len[0]) + 1)
			//			p.rxbuffer = append(p.rxbuffer, data...)
			//p.rxbuffer <- data
			//log.Println("6: channel " + hex.EncodeToString([]byte{channel}) + ": \n" + string(data))
		case byte(0x07):
			// payload
			length, err := p.readbyte(1)
			if err != nil {
				p.HandleIOError("reading payload length (byte 07)", err)
				return
			}
			data, err := p.readbyte(int(length[0]) + 1)
			if err != nil {
				p.HandleIOError("reading payload (byte 07)", err)
				return
			}
			p.rxbuffer <- data
			//log.Println("7: channel " + hex.EncodeToString([]byte{channel}) + ": " + hex.EncodeToString(data))

		}

		if err != nil {
			p.HandleIOError("reading reply to G-command", err)
			return
		}

		//check if we are still connected
		err = p.rawwrite(pactorch, 1, "L")
		if err != nil {
			p.HandleIOError("writing the L-command", err)
			return
		}
		_, err = p.readbyte(2)
		if err != nil {
			p.HandleIOError("reading reply to the L-command", err)
			return
		}
		// b should be 04 01
		l, err := p.readuntil(string(byte(0x00)))
		if err != nil {
			p.HandleIOError("reading data length after the L-command", err)
			return
		}
		status := l[len(l)-1]
		switch string(status) {
		case "0", "1", "3":
			// no connection
			writeDebug("Connection ended or lost. Code: " + string(status))
			p.endwa8ded()
			return
		}

		if len(p.txbuffer) <= maxtxbuffer {
			select {
			case p.rts <- struct{}{}:
				//
			default:
				//
			}

		}
		//handle commands
		//handle payload
		if len(p.txbuffer) != 0 {
			// send data to PACTOR
			//first check is modem is able to receive more data
			p.rawwrite(pactorch, 1, "L")
			_, err = p.readbyte(2)
			// b should be 04 01
			l, err := p.readuntil(string(byte(0x00)))
			if err != nil {
				p.HandleIOError("reading reply to the L-command while TX-ing", err)
				return
			}

			status := l[len(l)-1]
			//log.Println("Antwort auf  L: " + string(l) + "  status is: " + string(status))
			switch string(status) {
			case "0", "1", "3":
				//no connection
				writeDebug("connection ended while data was still in the buffer")
				p.txbuffer = nil
				p.endwa8ded()
				return
			case "4":
				trxdata := ""
				if len(p.txbuffer) > 254 {
					trxdata = string(p.txbuffer[:254])
					p.txbuffer = p.txbuffer[254:]
				} else {
					trxdata = string(p.txbuffer)
					p.txbuffer = nil
				}
				p.rawwrite(pactorch, 0, trxdata)
				//time.Sleep(200 * time.Millisecond)
				b, _ := p.readbyte(2)
				if b[0] != pactorch {
					writeDebug("CANNOT READ CHANNEL BACK! b is: " + hex.EncodeToString(b))
				}
				if b[1] != byte(0x00) {
					writeDebug("ERROR while sending, error code is: " + string(b))
				}
			default:
				// device is still busy (sending data), nothing we can about it here,
				// so just wait and poll again...
			}

		}
		if len(p.txbuffer) == 0 {
			//we need to test that again as the buffer might be empty after tx has been done.

			select {
			case p.rtd <- struct{}{}:
				//
			default:
				//
			}
		}
	}

}

func (p *pmodem) call() error {

	// The call() routine needs to be simplified:
	// Once we have a WA8DED mainloop running even without a connection (to support listen mode)
	// we should have a kind of "command buffer", then a call would be nothing else than a
	// command added to the command buffer.

	writeDebug("Calling " + p.remotecall)
	p.rawwrite(pactorch, 1, "C "+p.remotecall)
	//time.Sleep(100 * time.Millisecond)
	//	a, _ := p.readbyte(2)
	//	if a != []byte("\x04\x00") { //connect command failed, should rarely happen
	//		fmt.Println("PACTOR: Connect command failed")
	//		return errors.New("PACTOR Connect command failed")
	//	}

	connected := false
	for connected == false {
		p.rawwrite(pactorch, 1, "L")
		_, err := p.readbyte(2)
		// b should be 04 01, just discard
		l, err := p.readuntil(string(byte(0x00)))

		if err != nil {
			p.HandleIOError("reading reply to the L-command during link setup", err)
			p.Close()
		}
		if len(l) > 0 {
			status := l[len(l)-1]
			//log.Println("Antwort auf  L: " + string(l) + "  status is: " + string(status))
			switch string(status) {
			case "4":
				//connected
				connected = true
				return nil

			case "1":
				//Link setup
				//time.Sleep(1 * time.Second)
			case "0":
				//disconnected, no link
				writeDebug("no connection or connection timed out")
				return errors.New("connect timeout")

			}
		}
	}
	return nil
}
func (p *pmodem) pconnect() error {

	//initalize the buffers
	//p.rxbuffer = make([]byte, 0)
	p.rxbuffer = make(chan []byte, 8192)
	p.txbuffer = make([]byte, 0)
	p.rts = make(chan struct{})
	p.rtd = make(chan struct{})

	//register signals for SIGTERM handling (we at least want to try to set the PTC back to Terminal mode)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	//Setup serial device
	c := &serial.Config{Name: p.deviceName, Baud: p.baudRate, ReadTimeout: time.Second * serialtimeout}
	var err error
	p.device, err = serial.OpenPort(c)
	if err != nil {
		writeDebug(err.Error())
		return err
	}

	//go p.emulatorthread()

	// function to handle SIGINT, SIGTERM.
	// at the moment it just tries to leave the WA8DED hostmode
	go func() {
		sig := <-sigs
		writeDebug(sig.String() + "signal received, exiting")
		p.Close()
	}()
	p.writeexpect("QUIT", "cmd: ")
	p.writeexpect("MY "+p.mycall, "cmd: ")
	p.writeexpect("PTCH 4", "cmd: ")

	if p.init_script == "" {
		p.writeexpect("TONES 4", "cmd: ")
		p.writeexpect("PAC MON 0", "cmd: ")
	} else {
		file, err := os.Open(p.init_script)
		if err != nil {
			writeDebug(err.Error())
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			p.writeexpect(scanner.Text(), "cmd:")
		}

		if err := scanner.Err(); err != nil {
			writeDebug(err.Error())
			return err
		}
	}

	err = p.startwa8ded()
	//log.Println("Entered host mode")
	if err != nil {
		return errors.New("Cannot set PTC into WA8DED hostmode")
	}

	err = p.call()
	if err != nil {
		p.Close()
		p.err = err
		return p.err
	}

	go p.mainloop()
	return nil
}
