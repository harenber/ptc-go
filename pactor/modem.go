package pactor

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/albenik/go-serial/v2"
	"io"
	"log"
	"math/bits"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/howeyc/crc16"
)

const network = "pactor"

type Addr struct{ string }

var SCSversion = map[string]string{
	"A": "PTC-II",
	"B": "PTC-IIpro",
	"C": "PTC-IIe",
	"D": "PTC-IIex",
	"E": "PTC-IIusb",
	"F": "PTC-IInet",
	"H": "DR-7800",
	"I": "DR-7400",
	"K": "DR-7000",
	"L": "PTC-IIIusb",
	"T": "PTC-IIItrx",
}

type cstate struct {
	a int
	b int
	c int
	d int
	e int
	f int
}

type pflags struct {
	exit       bool
	stopmodem  bool
	listenMode bool
	//disconnected chan struct{}
	connected    chan struct{}
	closeWaiting chan struct{}
}

type pmux struct {
	device  sync.Mutex
	pactor  sync.Mutex
	write   sync.Mutex
	read    sync.Mutex
	close   sync.Mutex
	bufLen  sync.Mutex
	sendbuf sync.Mutex
	recvbuf sync.Mutex
}

type Modem struct {
	devicePath string

	localAddr  string
	remoteAddr string

	state State

	device        *serial.Port
	mux           pmux
	wg            sync.WaitGroup
	flags         pflags
	channelState  cstate
	goodChunks    int
	recvBuf       bytes.Buffer
	cmdBuf        chan string
	sendBuf       bytes.Buffer
	packetcounter bool
	chanbusy      bool
	cmdlineinit   string
	initfile      string
	closeOnce     sync.Once
}

func (p *Modem) SetDeadline(t time.Time) error      { return nil }
func (p *Modem) SetReadDeadline(t time.Time) error  { return nil }
func (p *Modem) SetWriteDeadline(t time.Time) error { return nil }

func (a Addr) Network() string { return network }
func (a Addr) String() string  { return a.string }

func (p *Modem) RemoteAddr() net.Addr { return Addr{p.remoteAddr} }
func (p *Modem) LocalAddr() net.Addr  { return Addr{p.localAddr} }

// Initialise the pactor modem and all variables. Switch the modem into hostmode.
//
// Will abort if modem reports failed link setup, Close() is called or timeout
// has occured (90 seconds)
func OpenModem(path string, baudRate int, myCall string, initfile string, cmdlineinit string) (p *Modem, err error) {

	p = &Modem{
		// Initialise variables
		devicePath: path,

		localAddr:  myCall,
		remoteAddr: "",

		state: Closed,

		device: nil,
		flags: pflags{
			exit:         false,
			stopmodem:    false,
			connected:    make(chan struct{}, 1),
			closeWaiting: make(chan struct{}, 1)},
		channelState: cstate{a: 0, b: 0, c: 0, d: 0, e: 0, f: 0},
		goodChunks:   0,
		cmdBuf:       make(chan string, 0),
		cmdlineinit:  cmdlineinit,
		initfile:     initfile,
	}

	writeDebug("Initialising pactor modem", 1)
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return nil, err
	}

	//Setup serial device
	if p.device, err = serial.Open(p.devicePath, serial.WithBaudrate(baudRate), serial.WithReadTimeout(SerialTimeout)); err != nil {
		writeDebug(err.Error(), 1)
		return nil, err
	}

	err = p.init()
	if err != nil {
		return nil, err
	}
	// run modem thread

	time.Sleep(3 * time.Second) // give the device a moment to settle, so no commands get lost
	return p, nil
}

func (p *Modem) setState(state State) {
	writeDebug("Setting state to: "+strconv.FormatUint(uint64(state), 10), 1)
	p.state = state
}

func (p *Modem) init() (err error) {
	writeDebug("Entering PACTOR init", 1)
	// Get modem out of CRC hostmode if it is still in it while starting.
	//p.stophostmode()
	//p._write(string([]byte{0xaa, 0xaa, 0x00, 0x01, 0x05, 0x4a, 0x48, 0x4f, 0x53, 0x54, 0x30, 0xfb, 0x3d}))
	//time.Sleep(100 * time.Millisecond)
	//_, _, _ = p._read(100)

	if _, _, err = p.writeAndGetResponse("", -1, false, 10240); err != nil {
		return err
	}

	// Make sure, modem is in main menu. Will respose with "ERROR:" when already in it -> Just discard answer!
	_, ans, err := p.writeAndGetResponse("Quit", -1, false, 1024)
	if err != nil {
		return err
	}

	if len(ans) < 2 {
		return errors.New("Modem does not react to Quit command. Please re-power your modem")
	}

	// Check if we can determine the modem's type
	_, ver, err := p.writeAndGetResponse("ver ##", -1, false, 1024)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(`\w#1`)
	if err != nil {
		return errors.New("Cannot read SCS modem version string, did you configure the correct serial device? Error: " + err.Error())
	}
	version := strings.ReplaceAll(string(re.Find([]byte(ver))), "#1", "")
	modem, exists := SCSversion[version]

	if !exists {
		return errors.New("Found a modem type: " + ver + " which this driver doesn't support. Please contact the author.")
	}
	writeDebug("Found a "+modem+" modem at "+p.devicePath, 0)
	writeDebug("Running init commands", 1)
	ct := time.Now()
	commands := []string{"DD", "RESTART", "MYcall " + p.localAddr, "PTCH " + strconv.Itoa(PactorChannel),
		"MAXE 35", "REM 0", "CHOB 0", "PD 1",
		"ADDLF 0", "ARX 0", "BELL 0", "BC 0", "BKCHR 25", "CMSG 0", "LFIGNORE 0", "LISTEN 0", "MAIL 0", "REMOTE 0",
		"PDTIMER 5", "STATUS 1",
		"TONES 4", "MARK 1600", "SPACE 1400", "CWID 0", "CONType 3", "MODE 0",
		"DATE " + ct.Format("020106"), "TIME " + ct.Format("150405")}

	//run additional commands provided on the command line with "init"
	if p.cmdlineinit != "" {
		for _, command := range strings.Split(strings.TrimSuffix(p.cmdlineinit, "\n"), "\n") {
			commands = append(commands, command)
		}
	}

	for _, cmd := range commands {
		var res string
		writeDebug("Sending command to modem: "+cmd, 0)
		_, res, err = p.writeAndGetResponse(cmd, -1, false, 1024)
		if err != nil {
			return err
		}
		if strings.Contains(res, "ERROR") {
			return fmt.Errorf(`Command "` + cmd + `" not accepted: ` + res)
		}
	}

	// run additional commands stored in the init script file
	if p.initfile != "" {
		if err = p.runInitScript(p.initfile); err != nil {
			return err
		}
	}
	p.flags.stopmodem = false
	p.setState(Ready)
	go p.modemThread()
	//p.flags.exit = false
	return nil
}

func (p *Modem) Addr() net.Addr {
	return p.LocalAddr()
}

// Send additional initialisation command stored in the InitScript
//
// Each command has to be on a new line
func (p *Modem) runInitScript(initScript string) error {
	if _, err := os.Stat(initScript); os.IsNotExist(err) {
		return fmt.Errorf("ERROR: PTC init script defined but not existent: %s", initScript)
	}

	file, err := os.Open(initScript)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		writeDebug("Sending command to modem: "+scanner.Text(), 0)
		if _, _, err = p.writeAndGetResponse(scanner.Text(), -1, false, 1024); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func (p *Modem) modemThread() {
	writeDebug("start modem thread", 1)

	_, _, _ = p.read(1024)
	_, _, _ = p.writeAndGetResponse("JHOST4", -1, false, 1024)
	_, _, _ = p.read(1024)
	// need to wait a second for WA8DED mode to "settle"
	time.Sleep(time.Second)
	const chunkSize = 1024
	p.wg.Add(1)
	var watchdog uint8
	for !p.flags.stopmodem {
		if watchdog == 0 {
			writeDebug("Modemthread ping", 1)
		}
		watchdog += 1
		if watchdog == 40 {
			watchdog = 0
		}
		// TX commands
		select {
		case cmd := <-p.cmdBuf:
			writeDebug("Write command ("+strconv.Itoa(len(cmd))+"): "+cmd, 1)
			_, ans, err := p.writeAndGetResponse(cmd, PactorChannel, true, chunkSize)
			if err != nil {
				writeDebug("Error when sending Command: "+err.Error(), 0)
			}
			writeDebug("Answer from modem: "+ans, 1)
			// TODO: Catch errors!
		default:

			// RX
			var res []byte

			if channels, err := p.getChannelsWithOutput(); err == nil {
				for _, c := range channels {
					_, data, _ := p.writeAndGetResponse("G", c, true, chunkSize)
					if _, res, err = p.checkResponse(data, c); err != nil {
						writeDebug("checkResponse returned: "+err.Error(), 1)
					} else {
						if res != nil {
							writeDebug("response: "+string(res)+"\n"+hex.Dump(res), 3)
							switch c {
							case PactorChannel:
								p.mux.recvbuf.Lock()
								p.recvBuf.Write(res)
								p.mux.recvbuf.Unlock()
								writeDebug("Written to sendbuf", 3)
							case 254: //Status update
								p.chanbusy = int(res[0])&112 == 112 // See PTC-IIIusb manual "STATUS" command
								writeDebug("PACTOR state: "+strconv.FormatInt(int64(res[0]), 2), 2)
							default:
								writeDebug("Channel "+strconv.Itoa(c)+": "+string(res), 1)
							}
						}
					}
				}
			}

			// TX data
			if p.getNumFramesNotTransmitted() < MaxFrameNotTX {
				data := p.getSendData()
				if len(data) > 0 {
					writeDebug("Write data ("+strconv.Itoa(len(data))+"): "+hex.EncodeToString(data), 2)
					// TODO: Catch errors!
					_, _, _ = p.writeAndGetResponse(string(data), PactorChannel, false, chunkSize)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _, err := p.writeAndGetResponse("JHOST0", PactorChannel, true, chunkSize)
	if err != nil {
		writeDebug("PACTOR ERROR Could not get modem out of the WA8DED mode: "+err.Error(), 0)
	}

	writeDebug("Modemthread is about to end", 1)

	time.Sleep(100 * time.Millisecond)
	p.wg.Done()
	//p.ModemClose()
	writeDebug("exit modem thread", 1)
}

func (p *Modem) Close() error {
	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("PACTOR Close called from "+file+"#"+strconv.Itoa(no), 1)
	} else {
		writeDebug("PACTOR Close called", 1)
	}

	var err error
	p.closeOnce.Do(func() { err = p.close() })
	return err
}
func (p *Modem) stophostmode() {
	writeDebug("Stopping WA8DED hostmode", 1)
	var ok bool
	var err error

	// Send JHOST0 in CRC-enhanced WA8DED mode syntax

	for ok == false {
		buff := make([]byte, 100)
		p.device.Write([]byte{0xaa, 0xaa, 0x00, 0x01, 0x05, 0x4a, 0x48, 0x4f, 0x53, 0x54, 0x30, 0xfb, 0x3d})
		time.Sleep(100 * time.Millisecond)
		for {
			writeDebug("Loop1", 1)
			n, err := p.device.Read(buff)
			if err != nil {
				log.Fatal(err)
				break
			}
			if n == 0 {
				break
			}
			//fmt.Printf("%v", string(buff[:n]))
		}
		p.device.Write([]byte("\rRESTART\r"))
		time.Sleep(1000 * time.Millisecond)
		for {
			writeDebug("Loop2", 1)
			n, err := p.device.Read(buff)
			if err != nil {
				log.Fatal(err)
				break
			}
			if n == 0 {
				break
			}
			//fmt.Printf("%v", string(buff[:n]))
		}
		ok, err = regexp.Match("cmd:", buff)
		if err != nil {
			writeDebug("Error in stophostmode: "+err.Error(), 0)
		}
	}
}

// Get data to be sent from the sendBuf channel.
//
// The amount of data bytes read from the sendBuf channel is limited by
// MaxSendData as the modem only accepts MaxSendData of bytes each command
func (p *Modem) getSendData() (data []byte) {
	buf := make([]byte, p.sendBuf.Len())
	_, _ = p.sendBuf.Read(buf)
	return buf
}

// Wait for transmission to be finished
//
// BLOCKING until either all frames are tranmitted and acknowledged or timeouts
// occures
func (p *Modem) waitTransmissionFinish(t time.Duration) error {
	timeout := time.After(t)
	tick := time.Tick(500 * time.Millisecond)
	for {
		notAck := p.getNumFrameNotAck()
		notTrans := p.getNumFramesNotTransmitted()
		select {
		case <-timeout:
			if notAck != 0 && notTrans != 0 {
				return errors.New("Timeout: " + strconv.Itoa(notAck) + "frames not ack and " + strconv.Itoa(notTrans) + " frames not transmitted")
			} else if notAck != 0 {
				return errors.New("Timeout: " + strconv.Itoa(notAck) + "frames not ack")
			} else {
				return errors.New("Timeout: " + strconv.Itoa(notTrans) + " frames not transmitted")
			}

		case <-tick:
			if notAck == 0 && notTrans == 0 {
				return nil
			}
		}
	}
}

// Call a remote target
//
// BLOCKING until either connected, pactor states disconnect or timeout occures
func (p *Modem) call(targetCall string) (err error) {
	if p.flags.listenMode {
		return fmt.Errorf("Cannot call a remote station on PACTOR while listen mode is active!")
	}
	/*	for p.state == Closing {
			time.Sleep(time.Second)
		}
		if p.state == Closed {
			err = p.init()
			if err != nil {
				return err
			}
		}*/
	//time.Sleep(time.Second) // need to wait here in case the modem got just into WA8DED mode. Otherwise, any further connection attempt fails
	p.remoteAddr = targetCall
	if err = p.connect(); err != nil {
		return err
	}

	select {
	case <-p.flags.connected:
		writeDebug("Link setup successful", 1)

	case <-time.After(90 * time.Second):
		p.forceDisconnect()
		return fmt.Errorf("Link setup timed out")
	}

	return nil
}

// Send connection request
func (p *Modem) connect() (err error) {
	writeDebug("PACTOR Connect request to "+p.remoteAddr, 1)
	err = p.send("C " + p.remoteAddr)
	return err
}

// Send disconnect request
//
// The modem tries to disconnect gracefully (e.g transmitting all remaining
// frames). Check with waitTransmissionFinish() for remaining or not acknowledged
// frames.
func (p *Modem) disconnect() (err error) {
	_, file, no, _ := runtime.Caller(1)
	writeDebug("PACTOR disconnect called from "+file+"#"+strconv.Itoa(no), 1)
	return p.send("D")
}

// Force disconnect
//
// Try to disconnect with disconnect() first.
func (p *Modem) forceDisconnect() (err error) {
	_, file, no, _ := runtime.Caller(1)
	writeDebug("PACTOR force disconnect called from "+file+"#"+strconv.Itoa(no), 1)
	writeDebug("PACTOR force discconnect command", 0)
	return p.send("DD")
}

// Send command to the pactor modem
func (p *Modem) send(msg string) error {
	if p.state == Closed {
		return errors.New("send: connection already closed")
	}
	p.cmdBuf <- msg
	writeDebug("wrote into the cmdbuf: "+msg, 1)
	return nil
}

// Close current serial connection. Stop all threads and close all channels.
func (p *Modem) close() (err error) {

	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("PACTOR close called from "+file+"#"+strconv.Itoa(no), 1)
	} else {
		writeDebug("PACTOR close called", 1)
	}

	p.disconnect()
	writeDebug("signal modem thread to stop", 1)
	p.flags.stopmodem = true
	writeDebug("waiting for all threads to exit", 1)
	p.wg.Wait()
	writeDebug("modem thread stopped", 1)

	//p.hostmodeQuit()
	// will not close the serial port as we reuse it
	//p.device.Close()
	p.sendBuf.Reset()
	p.recvBuf.Reset()
	//p.stophostmode()
	p.setState(Closed)
	writeDebug("PACTOR close() finished", 1)
	return nil
}

// Set specified flag (channel)
func setFlag(flag chan struct{}) {
	flag <- struct{}{}
}

// Get the number of Status Link messages not displayed (not read from modem)
func (p *Modem) getNumStatLinkMsgNotDispl() int {
	return p.channelState.a
}

// Get number of frames received but not displayed (not read from modem)
func (p *Modem) getNumFramesRecvNotDispl() int {
	return p.channelState.b
}

// Get number of frames not yet transmitted by the modem
func (p *Modem) getNumFramesNotTransmitted() int {
	return p.channelState.c
}

// Get number of frames not acknowledged by the remote station
func (p *Modem) getNumFrameNotAck() int {
	return p.channelState.d
}

// Check if serial device is still available (e.g still connected)
func (p *Modem) checkSerialDevice() (err error) {
	if _, err := os.Stat(p.devicePath); os.IsNotExist(err) {
		return fmt.Errorf("ERROR: Device %s does not exist", p.devicePath)
	}
	return nil
}

// Poll channel 255 with "G" command to find what channels have output.
// Write them into the channels list.
func (p *Modem) getChannelsWithOutput() (channels []int, err error) {
	//Poll Channel 255 to find what channels have output
	n, chs, err := p.writeAndGetResponse("G", 255, true, 1024)
	if err != nil {
		return nil, err
	}

	channels = make([]int, 0)

	for i, ch := range []byte(chs)[:n-1] {
		if (i == 0 && ch == 255) || (i == 1 && ch == 1) {
			continue
		}
		channels = append(channels, int(ch)-1)
	}

	writeDebug("Channels with output: "+fmt.Sprintf("%#v", channels), 2)

	return
}

// Query channel status ("L" command) of stated channel
//
// Returns struct of type cstate.
func (p *Modem) getChannelsStatus(ch int) (err error) {
	if ch == 0 {
		return fmt.Errorf("L-command for channel 0 not implemented")
	}
	// according to SCS folks, the L command does not work on PACTOR connections :-(

	_, stat, err := p.writeAndGetResponse("L", ch, true, 1024)
	if err != nil {
		return err
	}
	if len(stat) < 3 {
		return fmt.Errorf("No answer to the L-command")
	}
	writeDebug("Status string:\n"+hex.Dump([]byte(stat)), 3)
	s := strings.Split(strings.Replace(stat[2:], "\x00", "", -1), " ")
	if len(s) < 6 {
		return fmt.Errorf("L-command response to short")
	}

	p.channelState.a, _ = strconv.Atoi(s[0])
	p.channelState.b, _ = strconv.Atoi(s[1])
	p.channelState.c, _ = strconv.Atoi(s[2])
	p.channelState.d, _ = strconv.Atoi(s[3])
	p.channelState.e, _ = strconv.Atoi(s[4])
	if ch != PactorChannel {
		// SCS: not reliable in PACTOR mode
		p.channelState.f, _ = strconv.Atoi(s[5])
	}
	writeDebug(fmt.Sprintf("ChannelState: %+v", p.channelState), 2)
	/*else {
		_, stat, err := p.writeAndGetResponse("G3", 254, true, 1024)
		if err != nil {
			return err
		}
		writeDebug("G3 string:\n"+hex.Dump([]byte(stat)), 3)
		if _, res, _ := p.checkResponse(stat, 254); res != nil {
			if len(res) > 2 {
				if res[2] != 0 { //CONNECTED
					if p.channelState.f == 0 { //was disconnected
						writeDebug("PACTOR Connect level: "+string(res[2]+48), 0)
					}
					p.channelState.f = 4
					setFlag(p.flags.connected)
				} else {
					if p.channelState.f != 0 { //was disconnected
						writeDebug("PACTOR disconnected: "+string(res[2]+48), 0)
					}
					p.channelState.f = 0
				}
			}
		} else {
			writeDebug("G3 could not been evaluated", 3)
		}
	}*/

	return nil
}

// Do some checks on the returned data.
//
// Currently, only connection information (payload) messages are passed on
func (p *Modem) checkResponse(resp string, ch int) (n int, data []byte, err error) {
	if len(resp) < 3 {
		return 0, nil, fmt.Errorf("No data")
	}

	head := []byte(resp[:3])
	payload := []byte(resp[3:])
	length := int(head[2]) + 1
	pl := len(payload)
	if int(head[0]) != ch {
		writeDebug("WARNING: Returned data does not match polled channel\n"+hex.Dump([]byte(resp)), 1)
		return 0, nil, fmt.Errorf("Channel missmatch")
	}
	if int(head[1]) == 1 {
		writeDebug("*** SUCCESS: "+string(payload), 0)
	}
	if int(head[1]) == 2 {
		writeDebug("*** ERROR: "+string(payload), 0)
	}
	if int(head[1]) != 7 && int(head[1]) != 3 {
		writeDebug("Message from Modem: "+string(payload), 0)
		return 0, nil, fmt.Errorf("Not a data response")
	}
	if int(head[1]) == 3 { //Link status
		writeDebug("*** LINK STATUS: "+string(payload), 0)
		// there is no way to get the remote callsign from the WA8DED data, so we have to parse the link status :(
		re, err := regexp.Compile(` CONNECTED to \w{2,16}`)
		if err != nil {
			writeDebug("Cannot convert connect message to callsign: "+string(payload), 1)
		} else {
			p.channelState.f = 4 // mark station as being connected
			ans := strings.ReplaceAll(string(re.Find(payload)), " CONNECTED to ", "")
			if len(ans) > 2 { //callsign consists of 3+ characters
				p.remoteAddr = ans
				writeDebug("PACTOR connection to: "+ans, 0)
				setFlag(p.flags.connected)
				writeDebug("incoming connection flagged", 1)
				return 0, nil, nil
			}
		}
		if strings.Contains(string(payload), "DISCONNECTED") {
			writeDebug("PACTOR DISCONNECTED", 0)
			if !p.flags.listenMode {
				p.setState(ToBeClosed)
			}

			p.channelState.f = 0
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("Link data")
	}
	if length != pl {
		writeDebug("WARNING: Data length "+strconv.Itoa(pl)+" does not match stated amount "+strconv.Itoa(length)+". After "+strconv.Itoa(p.goodChunks)+" good chunks.", 1)
		p.goodChunks = 0
		if pl < length {
			// TODO: search for propper go function
			for i := 0; i < (length - pl); i++ {
				payload = append(payload, 0x00)
			}
		} else {
			payload = payload[:length]
		}
	} else {
		p.goodChunks += 1
		writeDebug("Good chunk", 2)
	}
	return length, payload, nil
}

// Write and read response from pactor modem
//
// Can be used in both, normal and hostmode. If used in hostmode, provide
// channel (>=0). If used in normal mode, set channel to -1, isCommand is
// ignored.
func (p *Modem) writeAndGetResponse(msg string, ch int, isCommand bool, chunkSize int) (int, string, error) {
	writeDebug("wagr: Channel: "+strconv.Itoa(ch)+"; isCommand: "+strconv.FormatBool(isCommand)+"\n"+hex.Dump([]byte(msg)), 3)
	var err error

	var n int
	var str string
	if ch < 0 {
		err = p._write(msg + "\r")
		if err != nil {
			return 0, "", err
		}
		time.Sleep(500 * time.Millisecond)
		i := 0
		var tmp []byte
		for {
			n, tmp, err = p._read(chunkSize)
			str = string(tmp)
			if err == nil {
				break
			}
			i += 1
			if i > 9 {
				writeDebug("No successful read after 10 times!", 1)
				return 0, "", err
			}
		}
		writeDebug(fmt.Sprintf("response: %s\n%s", str, hex.Dump(tmp)), 2)
		return n, str, err
	} else {
		err = p.writeChannel(msg, ch, isCommand)
		if err != nil {
			return 0, "", err
		}
		if msg == "JHOST0" { //looks like the SCS modems do not answer to JHOST0, although the standard defines it
			return 0, "", nil
		}
		time.Sleep(50 * time.Millisecond)
		writeDebug("Decode WA8DED", 4)
		buf := []byte{}
		valid := false

		for valid == false {
			if bytes.Compare(buf, []byte{170, 170, 170, 85}) == 0 { // check for re-request 	#170#170#170#85
				// packet was re-requested!!
				writeDebug("Re-Request magic received", 3)
				buf = []byte{}                           //delete the re-request packet
				err = p.writeChannel(msg, ch, isCommand) // write command again
				if err != nil {
					return 0, "", err
				}
			}
			br, b, err := p._read(1)
			if err != nil {
				writeDebug("ERROR at _read: "+error.Error(err), 1)
			}
			writeDebug("Len: "+strconv.Itoa(len(buf))+"State: "+string(hex.Dump(buf)+"\n"), 4)
			if br > 0 {
				//we got some data
				buf = append(buf, b...)
				if len(buf) > 5 { // otherwise it's no enh. WA8DED: 2 magic bytes, 2 header bytes, 2 CRC bytes
					//unstuff (from 3rd byte on)
					t := bytes.Replace(buf[2:], []byte{170, 0}, []byte{170}, -1)
					buf = append([]byte{0xaa, 0xaa}, t...)
					if checkcrc(buf) {
						n = len(t) - 2
						str = string(t[:n])
						valid = true
					} else {
						writeDebug("(still) Invalid checksum", 4)
					}

				}
			}
		}

	}

	p.packetcounter = !(p.packetcounter)
	writeDebug("wagr: returning \n"+hex.Dump([]byte(str)), 3)
	return n, str, err
}

// Read bytes from the Software receive sendbuf. The receive thread takes care of
// reading them from the pactor modem
//
// BLOCK until receive sendbuf has any data!
func (p *Modem) Read(d []byte) (int, error) {
	writeDebug("Read() called", 3)
	writeDebug("len(d) is: "+strconv.Itoa(len(d)), 3)
	if p.state == Closed {
		writeDebug("Reading from a closed PACTOR connection", 1)
		return 0, io.EOF
	}
	if p.state == ToBeClosed {
		writeDebug("Sending EOF to close connection after Call", 1)
		return 0, io.EOF
	}
	p.mux.read.Lock()
	defer p.mux.read.Unlock()

	if len(d) == 0 {
		return 0, nil
	}

	timeout := 0
	for p.recvBuf.Len() == 0 {
		if p.state == Closed {
			writeDebug("Reading from a closed PACTOR connection", 1)
			return 0, io.EOF
		}
		if p.state == ToBeClosed {
			writeDebug("Sending EOF to close connection after Call", 1)
			return 0, io.EOF
		}
		if p.channelState.f == 0 {
			writeDebug("sending EOF as connection is not active", 1)
			return 0, io.EOF
		}
	}
	p.mux.recvbuf.Lock()
	l := p.recvBuf.Len()
	p.mux.recvbuf.Unlock()
	data := make([]byte, l)
	for {
		writeDebug("p.recvBuf.Len: "+strconv.Itoa(l), 3)
		if p.channelState.f == 0 {
			writeDebug("Read() returns io.EOF", 3)
			return 0, io.EOF
		}
		if l > 0 {
			p.mux.recvbuf.Lock()
			nn, err := p.recvBuf.Read(data)
			p.mux.recvbuf.Unlock()
			writeDebug("Read "+strconv.Itoa(nn)+" from sendbuf", 3)
			if err != nil {
				writeDebug(err.Error(), 3)
			}
			break
		}
		if timeout > 30 {
			writeDebug("PACTOR Read() timeout after 30 sec", 3)
			break
		}
		timeout += 1
		time.Sleep(time.Second)
	}

	for i, b := range data {
		d[i] = byte(b)
	}

	writeDebug("Read() len: "+strconv.Itoa(len(data))+" cap: "+strconv.Itoa(cap(data))+" returned: "+string(data), 3)
	return len(data), nil

}

// Write bytes to the software send sendbuf. The send thread will take care of
// fowarding them to the pactor modem
//
// BLOCK if send sendbuf is full! Remains as soon as there is space left in the
// send sendbuf
func (p *Modem) Write(d []byte) (int, error) {
	writeDebug("Write() len: "+strconv.Itoa(len(d))+" data: "+string(d), 3)
	if p.state == Closed {
		writeDebug("Writing to a close PACTOR connection", 1)
		return 0, io.EOF
	}
	p.mux.write.Lock()
	defer p.mux.write.Unlock()

	for p.sendBuf.Len()+len(d) > MaxSendData {
		// wait until buffer got empty enough to add the data
		time.Sleep(100 * time.Millisecond)
	}
	if p.channelState.f == 0 {
		return 0, fmt.Errorf("Read from closed connection")
	}

	for _, b := range d {
		if p.sendBuf.Len() > MaxSendData {
			time.Sleep(time.Second)
		} else {
			p.mux.sendbuf.Lock()
			p.sendBuf.WriteByte(b)
			p.mux.sendbuf.Unlock()
		}
		/*select {
		case <-p.flags.closeWaiting:
			return 0, fmt.Errorf("Writing on closed connection")
		case p.sendBuf <- b:
		}*/

		/*p.mux.bufLen.Lock()
		p.sendBufLen++
		p.mux.bufLen.Unlock()*/
	}

	return len(d), nil
}

// Flush waits for the last frames to be transmitted.
//
// Will throw error if remaining frames could not bet sent within 120s
func (p *Modem) Flush() (err error) {
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
/*func (p *Modem) ModemClose() error {
	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("PACTOR ModemClose called from "+file+"#"+strconv.Itoa(no), 1)
	} else {
		writeDebug("PACTOR ModemClose called", 1)
	}
	//prevent close from being called too often
	if p.state == Closing {
		writeDebug("Close() call already running", 1)
		time.Sleep(time.Second)
		return nil // not sure if one would better call io.EOF here
	}
	if p.state == Closed {
		writeDebug("Close() called on a already closed interface", 1)
		return nil
	}
	p.setState(Closing)

	if p.channelState.f != 0 {
		p.disconnect()
		//Wait for the modem to change state from connected to disconnected
		timeout := 0
		for {
			timeout += 1
			if timeout <= 20 {
				if p.channelState.f == 0 {
					writeDebug("Disconnect successful", 1)
					//if !p.flags.listenMode {
					p.close()
					time.Sleep(500 * time.Millisecond) // wait half a second
					writeDebug("Close() finished.", 1)
					return nil
				}
			} else {
				p.forceDisconnect()
				time.Sleep(500 * time.Millisecond) // wait half a second
				p.close()
				//p = nil
				return nil
			}
			time.Sleep(time.Second)
		}
	} else { // PACTOR modem is _NOT_ connected and Close() is called, so probably the user wants to end listen mode
		if p.flags.listenMode {
			writeDebug("Close called in ListenMode", 1)
			setFlag(p.flags.closeWaiting)
			p.close()
			return nil
		}
		p.close()
		writeDebug("Close() finished.", 1)
		return nil

	}
	writeDebug("end of Close reached, shouldn't happen", 0)
	return nil
}
*/
// TxBufferLen returns the number of bytes in the out sendbuf queue.
func (p *Modem) TxBufferLen() int {
	p.mux.bufLen.Lock()
	defer p.mux.bufLen.Unlock()
	writeDebug("TxBufferLen called ("+strconv.Itoa(p.sendBuf.Len())+" bytes remaining in sendbuf)", 2)
	return p.sendBuf.Len() + (p.getNumFramesNotTransmitted() * MaxSendData)
}

func (p *Modem) Busy() bool {
	return p.chanbusy
}

// Write to serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) write(cmd string) error {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._write(cmd)
}

// Write to serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) _write(cmd string) error {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return err
	}

	writeDebug("write: \n"+hex.Dump([]byte(cmd)), 3)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	out := cmd + "\r"
	for {
		status, err := p.device.GetModemStatusBits()
		if err != nil {
			writeDebug("GetModemStatusBits failed. cmd: "+cmd+" Error: "+err.Error(), 1)
			return err
		}
		if status.CTS {
			for {
				sent, err := p.device.Write([]byte(out))
				if err == nil {
					break
				} else {
					//					log.Errorf("ERROR while sending serial command: %s\n", out)
					writeDebug(err.Error(), 2)
					out = out[sent:]
				}
			}
			break
		}
	}

	return nil
}

// Write channel to serial connection (thread safe)
//
// No other read/write operation allowed during this time
/*func (p *Modem) writeChannel(msg string, ch int, isCommand bool) error {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._writeChannel(msg, ch, isCommand)
}*/

// *** Helper functions for the CRC hostmode
// Allthough these functions are small, I prefer to keep their functionality
// separate. They follow the steps in the SCS documentation

// helper function: de-hexlify and write to debug channel
func printhex(s string) {
	t, _ := hex.DecodeString(s)
	writeDebug(string(t), 3)
}

// helper function: "unstuff" a string
func unstuff(s string) string {
	//Expect: the string contains aa aa at the beginning, that should NOT be
	//stuffed
	n, _ := hex.DecodeString(s[4:])
	n = bytes.Replace(n, []byte{170, 0}, []byte{170}, -1)
	var r []byte
	r = append([]byte{0xaa, 0xaa}, n...)
	re := hex.EncodeToString(r)
	return re
}

// helper function: "stuff" a string: replaces every #170 with #170#0
func stuff(s string) string {
	//Expect: the string contains aa aa at the beginning, that should NOT be
	//stuffed
	n, err := hex.DecodeString(s[4:])
	if err != nil {
		writeDebug("ERROR in Stuff: "+err.Error()+"\n"+string(hex.Dump([]byte(s))), 1)
	}

	n = bytes.Replace(n, []byte{170}, []byte{170, 0}, -1)
	var r []byte
	r = append([]byte{0xaa, 0xaa}, n...)
	re := hex.EncodeToString(r)
	return re
}

// helper function: calculates the CCITT-CRC16 checksum
func checksum(s string) uint16 {
	tochecksum, _ := hex.DecodeString(s[4:])
	chksum := bits.ReverseBytes16(crc16.ChecksumCCITT([]byte(tochecksum)))
	return chksum
}

// helper fuction: check the checksum by comparing
func checkcrc(s []byte) bool {
	tochecksum := s[2 : len(s)-2]
	chksum := bits.ReverseBytes16(crc16.ChecksumCCITT(tochecksum))
	pksum := s[len(s)-2:]
	return (binary.BigEndian.Uint16(pksum) == chksum)
}

// super helper fuction: convert an ordinary WA8DED message into a CRC-Hostmode message
func docrc(msg string) string {
	// step 1: add a #170170
	msg = fmt.Sprintf("%02x%02x%s", 170, 170, msg)
	// step 2: calculate and add the checksum
	chksum := checksum(msg)
	msg = fmt.Sprintf("%s%04x", msg, chksum)
	// step 3: add "stuff" bytes
	msg = stuff(msg)
	return msg

}

// Write channel to serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) writeChannel(msg string, ch int, isCommand bool) error {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return err
	}

	var d byte
	switch {
	case !p.packetcounter && !isCommand:
		d = byte(0x00)
	case !p.packetcounter && isCommand:
		d = byte(0x01)
	case p.packetcounter && !isCommand:
		d = byte(0x80)
	case p.packetcounter && isCommand:
		d = byte(0x81)
	}

	var o []byte = []byte{170, 170, byte(ch), byte(d), byte((len(msg) - 1))}
	o = append(o, []byte(msg)...)
	cksum := bits.ReverseBytes16(crc16.ChecksumCCITT(o[2:]))
	cksumb := make([]byte, 2)
	binary.BigEndian.PutUint16(cksumb, cksum)
	o = append(o, cksumb...)
	tostuff := o[2:]
	tostuff = bytes.Replace(tostuff, []byte{170}, []byte{170, 0}, -1)
	o = append([]byte{170, 170}, tostuff...)
	/*
		msg = fmt.Sprintf("%02x%02x%s", 170, 170, msg)
		// step 2: calculate and add the checksum
		chksum := checksum(msg)
		msg = fmt.Sprintf("%s%04x", msg, chksum)
		// step 3: add "stuff" bytes
		msg = stuff(msg)
	*/
	/*c := fmt.Sprintf("%02x", ch)
	l := fmt.Sprintf("%02x", (len(msg) - 1))
	s := hex.EncodeToString([]byte(msg))
	// add crc hostmode addons
	bs, _ := hex.DecodeString(docrc(fmt.Sprintf("%s%s%s%s", c, d, l, s)))*/
	writeDebug("write channel: \n"+hex.Dump(o), 2)

	if err := p.write(string(o)); err != nil {
		writeDebug(err.Error(), 2)
		return err
	}
	/*if !isCommand {
		p.cmdBuf <- "%Q"
	}*/
	return nil
}

// Read from serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) read(chunkSize int) (int, []byte, error) {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._read(chunkSize)
}

// Read from serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) _read(chunkSize int) (int, []byte, error) {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return 0, []byte{}, err
	}

	buf := make([]byte, chunkSize)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	n, err := p.device.Read(buf)
	if err != nil {
		writeDebug("Error received during read: "+err.Error(), 1)
		return 0, []byte{}, err
	}

	return n, buf[0:n], nil
}
