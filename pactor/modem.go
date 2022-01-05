package pactor

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albenik/go-serial/v2"
	"github.com/howeyc/crc16"
)

const network = "pactor"

type Addr struct{ string }

type cstate struct {
	a int
	b int
	c int
	d int
	e int
	f int
}

type pflags struct {
	exit         bool
	closeCalled  bool
	closed       bool
	listenMode   bool
	disconnected chan struct{}
	connected    chan struct{}
	closeWriting chan struct{}
}

type pmux struct {
	device sync.Mutex
	pactor sync.Mutex
	write  sync.Mutex
	read   sync.Mutex
	close  sync.Mutex
	bufLen sync.Mutex
}

type Modem struct {
	devicePath string

	localAddr  string
	remoteAddr string

	state    State
	stateOld State

	device        *serial.Port
	mux           pmux
	wg            sync.WaitGroup
	flags         pflags
	channelState  cstate
	goodChunks    int
	recvBuf       chan []byte
	cmdBuf        chan string
	sendBuf       chan byte
	sendBufLen    int
	packetcounter bool
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
func OpenModem(path string, baudRate int, myCall string, initScript string, cmdlineinit string) (p *Modem, err error) {

	p = &Modem{
		// Initialise variables
		devicePath: path,

		localAddr:  myCall,
		remoteAddr: "",

		state:    Unknown,
		stateOld: Unknown,

		device: nil,
		flags: pflags{
			exit:         false,
			closeCalled:  false,
			disconnected: make(chan struct{}, 1),
			connected:    make(chan struct{}, 1),
			closeWriting: make(chan struct{})},
		channelState: cstate{a: 0, b: 0, c: 0, d: 0, e: 0, f: 0},
		goodChunks:   0,
		recvBuf:      make(chan []byte, 0),
		cmdBuf:       make(chan string, 0),
		sendBuf:      make(chan byte, MaxSendData),
		sendBufLen:   0,
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

	p.hostmodeQuit() // throws error if not in hostmode (e.g. from previous connection)
	if _, _, err = p.writeAndGetResponse("", -1, false, 10240); err != nil {
		return nil, err
	}

	// Make sure, modem is in main menu. Will respose with "ERROR:" when already in
	// main menu!
	_, _, err = p.writeAndGetResponse("Quit", -1, false, 1024)
	if err != nil {
		return nil, err
	}

	writeDebug("Running init commands", 1)
	ct := time.Now()
	commands := []string{"MYcall " + p.localAddr, "PTCH " + strconv.Itoa(PactorChannel),
		"MAXE 35", "REM 0", "CHOB 0",
		"TONES 4", "MARK 1600", "SPACE 1400", "CWID 0", "CONType 3", "MODE 0",
		"DATE " + ct.Format("020106"), "TIME " + ct.Format("150405")}

	//run additional commands provided on the command line with "init"
	if cmdlineinit != "" {
		for _, command := range strings.Split(strings.TrimSuffix(cmdlineinit, "\n"), "\n") {
			commands = append(commands, command)
		}
	}

	for _, cmd := range commands {
		var res string
		_, res, err = p.writeAndGetResponse(cmd, -1, false, 1024)
		if err != nil {
			return nil, err
		}
		if strings.Contains(res, "ERROR") {
			return nil, fmt.Errorf(`Command "` + cmd + `" not accepted: ` + res)
		}
	}

	// run additional commands stored in the init script file
	if initScript != "" {
		if err = p.runInitScript(initScript); err != nil {
			return nil, err
		}
	}

	p.hostmodeStart() // throws error when switching successfully to hostmode

	if err = p.runControlLoops(); err != nil {
		return nil, err
	}

	return p, nil
}

func (p *Modem) reInit() error {
	writeDebug("Re-initialzing PACTOR modem. Waiting for threads to finish", 1)
	p.wg.Wait()
	p.flags.closed = false
	p.flags.exit = false
	writeDebug("Re-initialzing PACTOR modem.", 1)

	p.recvBuf = make(chan []byte, 0)         // we have to create a new recvBuf
	p.sendBuf = make(chan byte, MaxSendData) // clean sendBuf
	p.sendBufLen = 0
	p.state = Unknown
	p.stateOld = Unknown
	p.remoteAddr = ""

	p.hostmodeQuit() // throws error if not in hostmode (e.g. from previous connection)
	if _, _, err := p.writeAndGetResponse("", -1, false, 10240); err != nil {
		writeDebug(err.Error(), 0)
		return err
	}

	// Make sure, modem is in main menu. Will respose with "ERROR:" when already in
	// main menu!
	_, _, err := p.writeAndGetResponse("Quit", -1, false, 1024)
	if err != nil {
		writeDebug(err.Error(), 0)
		return err
	}
	p.hostmodeStart() // throws error when switching successfully to hostmode

	if err := p.runControlLoops(); err != nil {
		writeDebug(err.Error(), 0)
		return err
	}

	return nil

}

// Call a remote target
//
// BLOCKING until either connected, pactor states disconnect or timeout occures
func (p *Modem) call(targetCall string) (err error) {
	if p.flags.listenMode {
		return fmt.Errorf("Cannot call a remote station on PACTOR while listen mode is active!")
	}
	p.remoteAddr = targetCall
	if err = p.connect(); err != nil {
		return err
	}

	select {
	case <-p.flags.connected:
		writeDebug("Link setup successful", 1)
	case <-p.flags.disconnected:
		p.close()
		return fmt.Errorf("Link setup failed")
	case <-time.After(90 * time.Second):
		p.close()
		return fmt.Errorf("Link setup timed out")
	}

	return nil
}

// Accept: when in listening mode, BLOCK until a connection is received.
func (p *Modem) Accept() (net.Conn, error) {
	writeDebug("Enetering listener Accept method", 1)
	p.flags.listenMode = true
	if p.flags.exit {
		p.reInit()
	}
	select {
	case <-p.flags.connected:
		writeDebug("incoming connection received", 1)
		return p, nil
	}
	p.flags.listenMode = false
	return nil, nil
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
		if _, _, err = p.writeAndGetResponse(scanner.Text(), -1, false, 1024); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// Start all three controll threads: status thread, send thrad and read thread
//
// Wait 1 second to allow all thread to start working before sending
// data/commands or receiving form the internal buffers
func (p *Modem) runControlLoops() error {
	p.wg.Add(3)

	go p.statusThread()
	go p.receiveThread()
	go p.sendThread()

	return nil
}

// Status thread: Monitoring the pactor status, handling changes in link state
//
// Is run twice the frequency of send/receive thread
func (p *Modem) statusThread() {
	writeDebug("start status thread", 1)
	for {
		if p.flags.exit {
			writeDebug("Bailing out of status thread", 1)
			break
		}

		p.updatePactorState()
		p.eventHandler()

		time.Sleep(500 * time.Millisecond)
	}
	p.wg.Done()
	writeDebug("exit status thread", 1)
}

// Receive thread: receiving data from modem if available
//
// Pactor modem status must be "connected". New data is written to the recv_buffer
// channel. BLOCKING until recvBuf has been read (e.g. by Read() function)
func (p *Modem) receiveThread() {
	writeDebug("start receive thread", 1)

	for p.flags.exit == false {
		writeDebug("ping receive thread", 3)

		time.Sleep(1000 * time.Millisecond)

		if p.state == Connected {
			var res []byte
			chunkSize := 10240

			if channels, err := p.getChannelsWithOutput(); err == nil {
				for _, c := range channels {
					if c == PactorChannel {
						_, data, _ := p.writeAndGetResponse("G", c, true, chunkSize)
						if _, res, _ = p.checkResponse(data, c); res != nil {
							p.recvBuf <- res
							writeDebug("response: "+string(res)+"\n"+hex.Dump(res), 3)
						}
						break
					}
				}
			}
		}

		// if p.flags.exit {
		// 	writeDebug("exiting receive thread", 1)
		// 	// The recvBuf needs to close so that pat detects the end of the lifetime of the connection
		// 	// reInit will be called...
		// 	close(p.recvBuf)
		// 	break
		// }

	}
	// The recvBuf needs to be closed so that pat detects the end of the lifetime of the connection
	// reInit will be called...
	close(p.recvBuf)
	p.wg.Done()
	writeDebug("exit receive thread", 1)
}

// Send thread: Write data or command into the transmit buffer of the modem
//
// Commands are sent imediately where as payload (e.g. written by Write()) is
// held back until maximum number of frames not transmitted drops below
// MaxFrameNotTX
func (p *Modem) sendThread() {
	writeDebug("start send thread", 1)

	for {
		if p.flags.exit {
			writeDebug("exiting send thread", 1)
			close(p.sendBuf)
			p.sendBufLen = 0
			break
		}

		select {
		case cmd := <-p.cmdBuf:
			writeDebug("Write ("+strconv.Itoa(len(cmd))+"): "+cmd, 2)
			p.writeChannel(cmd, PactorChannel, true)
		default:
		}

		if p.getNumFramesNotTransmitted() < MaxFrameNotTX {
			data := p.getSendData()
			if len(data) > 0 {
				writeDebug("Write ("+strconv.Itoa(len(data))+"): "+hex.EncodeToString(data), 2)
				p.writeChannel(string(data), PactorChannel, false)
			}
		}

		time.Sleep(1000 * time.Millisecond)
	}
	p.wg.Done()
	writeDebug("exit send thread", 2)
}

// Get data to be sent from the sendBuf channel.
//
// The amount of data bytes read from the sendBuf channel is limited by
// MaxSendData as the modem only accepts MaxSendData of bytes each command
func (p *Modem) getSendData() (data []byte) {
	count := 0
	for {
		if count < MaxSendData {
			select {
			case b := <-p.sendBuf:
				data = append(data, b)
				count++
				p.mux.bufLen.Lock()
				p.sendBufLen--
				p.mux.bufLen.Unlock()
			default:
				writeDebug("No more data to send after "+strconv.Itoa(count)+" bytes", 3)
				return
			}
		} else {
			writeDebug("Reached max send data size ("+strconv.Itoa(count)+" bytes)", 3)
			return
		}
	}
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

// Send connection request
func (p *Modem) connect() (err error) {
	return p.send("C " + p.remoteAddr)
}

// Send disconnect request
//
// The modem tries to disconnect gracefully (e.g transmitting all remaining
// frames). Check with waitTransmissionFinish() for remaining or not acknowledged
// frames.
func (p *Modem) disconnect() (err error) {
	writeDebug("PACTOR disconnect command", 0)
	return p.send("D")
}

// Force disconnect
//
// Try to disconnect with disconnect() first. Clears all data remaining in
// modem transmission buffer
func (p *Modem) forceDisconnect() (err error) {
	writeDebug("PACTOR force discconnect command", 0)
	return p.send("DD")
}

// Send command to the pactor modem
func (p *Modem) send(msg string) error {
	p.cmdBuf <- msg
	return nil
}

// Close current serial connection. Stop all threads and close all channels.
func (p *Modem) close() (err error) {
	// if p.flags.closed {
	// 	return nil
	// }
	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("PACTOR close called from "+file+"#"+strconv.Itoa(no), 1)
	} else {
		writeDebug("PACTOR close called", 1)
	}

	p.flags.closed = true
	p.flags.exit = true
	// Channels shouldn't be closed anymore b/c listen mode requires us to keep it the modem structure open
	// close(p.flags.closeWriting)
	// close(p.flags.disconnected)
	// close(p.flags.connected)
	// close(p.cmdBuf)
	// close(p.sendBuf)

	writeDebug("waiting for all threads to exit", 1)
	p.wg.Wait()

	p.hostmodeQuit()
	// The serial device shouldn't be closed anymore b/c listen mode requires us to keep it the modem structure open
	//p.device.Close()
	writeDebug("PACTOR close() finsihed", 1)
	return nil
}

// Start modem hostemode (CRC Hostmode)
func (p *Modem) hostmodeStart() error {
	writeDebug("start hostmode", 1)
	_, _, err := p.writeAndGetResponse("JHOST4", -1, false, 1024)
	_, _, err = p.read(1024)
	return err
}

// Quit modem hostmode
func (p *Modem) hostmodeQuit() error {
	//wait a bit for other commands to finish
	time.Sleep(100 * time.Microsecond)
	_, _, err := p.writeAndGetResponse("JHOST0", 0, true, 1024)
	_, _, err = p.writeAndGetResponse("", -1, false, 1024)
	return err
}

// Restart the pactor modem
func (p *Modem) restart() error {
	_, _, err := p.writeAndGetResponse("RESTART", -1, false, 1024)
	return err
}

// Update the pactor state
//
// Set the pactor state according to the state polled form modem
func (p *Modem) updatePactorState() (err error) {
	p.channelState, err = p.getChannelsStatus(PactorChannel)
	if err != nil {
		writeDebug("Could not read Pactor state", 1)
		return err
	}

	debugLevel := 3

	switch p.channelState.f {
	case 0:
		writeDebug("Pactor: Disconnected", debugLevel)
		p.state = Disconnected

	case 1:
		writeDebug("Pactor: Link Setup", debugLevel)
		p.state = LinkSetup

	case 2:
		writeDebug("Pactor: Frame Reject", debugLevel)
		p.state = Connected

	case 3:
		writeDebug("Pactor: Disconnect request", debugLevel)
		p.state = DisconnectReq

	case 4:
		writeDebug("Pactor: Information transfer", debugLevel)
		p.state = Connected

	case 5:
		writeDebug("Pactor: Reject frame sent", debugLevel)
		p.state = Connected

	case 6:
		writeDebug("Pactor: Waiting acknowledgement", debugLevel)
		p.state = Connected

	default:
		writeDebug("Pactor: Unkown state", 1)

	}

	return nil
}

// Handle changes in pactor state (events)
//
// Set/clear flags according to the event occured. Try to close connection
// gracefully if connection is lost or disconnect request has been received
func (p *Modem) eventHandler() {
	if p.state != p.stateOld {
		if p.state == Connected {
			setFlag(p.flags.connected)
			writeDebug("Connection established", 1)
		}
		if p.stateOld == LinkSetup && p.state == Disconnected {
			setFlag(p.flags.disconnected)
			writeDebug("Link setup failed", 1)
		}
		if p.stateOld == Connected && p.state == Disconnected ||
			p.stateOld == DisconnectReq && p.state == Disconnected {
			if p.flags.closeCalled != true {
				p.flags.closeCalled = true
				writeDebug("Connection lost", 1)
				p.flags.exit = true
				//go p.Close() //run as separate thread in order not to block
			}
			setFlag(p.flags.disconnected)
			writeDebug("Disconnect occured", 1)
			p.flags.exit = true
		}
		if p.stateOld == Connected && p.state == DisconnectReq {
			writeDebug("Disconnect requested", 1)
			//go p.Close() //run as separate thread in order not to block
		}

		p.stateOld = p.state
	}
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

	writeDebug("Channels with output: "+fmt.Sprintf("%#v", channels), 3)

	return
}

// Query channel status ("L" command) of stated channel
//
// Returns struct of type cstate.
func (p *Modem) getChannelsStatus(ch int) (channelState cstate, err error) {
	if ch == 0 {
		return cstate{}, fmt.Errorf("L-command for channel 0 not implemented")
	}

	_, stat, err := p.writeAndGetResponse("L", ch, true, 1024)
	if err != nil {
		return cstate{}, err
	}
	if len(stat) < 3 {
		return cstate{}, fmt.Errorf("No answer to the L-command")
	}
	writeDebug("Status string:\n"+hex.Dump([]byte(stat)), 3)
	s := strings.Split(strings.Replace(stat[2:], "\x00", "", -1), " ")
	if len(s) < 6 {
		return cstate{}, fmt.Errorf("L-command response to short")
	}

	channelState.a, _ = strconv.Atoi(s[0])
	channelState.b, _ = strconv.Atoi(s[1])
	channelState.c, _ = strconv.Atoi(s[2])
	channelState.d, _ = strconv.Atoi(s[3])
	channelState.e, _ = strconv.Atoi(s[4])
	channelState.f, _ = strconv.Atoi(s[5])

	writeDebug(fmt.Sprintf("ChannelState: %+v", channelState), 2)

	return channelState, nil
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
	if int(head[1]) != 7 && int(head[1]) != 3 {
		writeDebug("Not a data response: "+string(payload), 1)
		return 0, nil, fmt.Errorf("Not a data response")
	}
	if int(head[1]) == 3 { //Link status
		// there is no way to get the remote callsign from the WA8DED data, so we have to parse the link status :(
		re, err := regexp.Compile(` CONNECTED to \w{2,16}`)
		if err != nil {
			writeDebug("Cannot convert connect message to callsign: "+string(payload), 1)
		} else {
			ans := strings.ReplaceAll(string(re.Find(payload)), " CONNECTED to ", "")
			if len(ans) > 2 { //callsign consists of 3+ characters
				p.remoteAddr = ans
				writeDebug("PACTOR connection to: "+ans, 0)
			}
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
	var err error

	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()

	if ch >= 0 {
		err = p._writeChannel(msg, ch, isCommand)
	} else {
		err = p._write(msg)
	}

	if err != nil {
		return 0, "", err
	}

	// allow the message/command to be processed before reading the response
	time.Sleep(200 * time.Millisecond)

	var n int
	var str string
	i := 0
	for {
		n, str, err = p._read(chunkSize)
		if err == nil {
			break
		}
		i += 1
		if i > 9 {
			writeDebug("No successful read after 10 times!", 1)
			return 0, "", err
		}
	}
	writeDebug("response: "+str+"\n"+hex.Dump([]byte(str)), 2)
	if ch >= 0 {

		// check for re-request 	#170#170#170#85
		for {
			if str != fmt.Sprintf("%c%c%c%c", 170, 170, 170, 85) {
				break
			}
			// packet was re-requested!!
			writeDebug("Packet re-request received!", 2)
			err = p._writeChannel(msg, ch, isCommand)
			if err != nil {
				return 0, "", err
			}
			time.Sleep(200 * time.Millisecond)
			n, str, err = p._read(chunkSize)
		}
		// check then length to prevent out of range panics
		// if the answer is <=2 bytes, it's not a CRC hostmode packet
		if len(str) > 2 {
			// CRC Hostmode decoding
			// we "hexlify" it first, that makes it easier to debug
			// then we rely on helper functions
			hexstring := hex.EncodeToString([]byte(str))
			// Step 1: destuff
			hexstring = unstuff(hexstring)
			// Step 2: check CRC
			if checkcrc(hexstring) == false {
				writeDebug("Wrong checksum in package!", 1)
				return 0, "", errors.New("Checksum error at receiving packet")
			}

			//remove #170#170 at the beginning at chksum at the end
			hexstring = hexstring[4 : len(hexstring)-4]
			r, err := hex.DecodeString(hexstring)
			if err != nil {
				return 0, "", err
			}
			p.packetcounter = !(p.packetcounter)
			str = string(r)
			n = len(str)
		} else {
			//packet is too short to be a real WA8DED packet
			return 0, "", errors.New("packet too short to be a WA8DED packet")
		}

	}
	return n, str, err
}

// Read bytes from the Software receive buffer. The receive thread takes care of
// reading them from the pactor modem
//
// BLOCK until receive buffer has any data!
func (p *Modem) Read(d []byte) (int, error) {
	p.mux.read.Lock()
	defer p.mux.read.Unlock()

	if p.state != Connected {
		//TODO: check if we need an error here!
		return 0, fmt.Errorf("Read from closed connection")
		//return 0, nil
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

	if p.state != Connected {
		return 0, fmt.Errorf("Read from closed connection")
	}

	for _, b := range d {
		select {
		case <-p.flags.closeWriting:
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
	if p.state != Connected {
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
	_, file, no, ok := runtime.Caller(1)
	if ok {
		writeDebug("PACTOR Close called from "+file+"#"+strconv.Itoa(no), 1)
	} else {
		writeDebug("PACTOR Close called", 1)
	}

	if p == nil { // already closed, nothing to do.
		writeDebug("Modem instance already closed.", 1)
		return nil
	}

	//	if p.flags.closed {
	//		writeDebug("PACTOR Close() called on a closed connection", 1)
	//		return errors.New("Close called on a closed connection")
	//	}

	p.mux.close.Lock()
	defer p.mux.close.Unlock()

	// force update PACTOR state
	_ = p.updatePactorState()
	//	if p.flags.closeCalled != true {
	p.flags.listenMode = false
	if true {

		//p.flags.closeCalled = true
		switch p.state {
		case Connected:
			writeDebug("close called while still connected", 2)
			// Connected to remote, try to send remaining frames and disconnect
			// gracefully

			// Wait for remaining data to be transmitted and acknowledged
			// if err := p.waitTransmissionFinish(90 * time.Second); err != nil {
			// 	writeDebug(err.Error(), 2)
			// }

			p.disconnect()

			// Wait for disconnect command to be transmitted and acknowledged
			// if err := p.waitTransmissionFinish(30 * time.Second); err != nil {
			// 	writeDebug(err.Error(), 2)
			// }

		case Disconnected:
			p.flags.exit = true
			p.close()
			return nil

		case Unknown:
			return nil

		default:
			// Link Setup (connection) not yet successful,...
			p.forceDisconnect()
			p.flags.exit = true
			p.close()
			return nil
		}
	}

	//Wait for the modem to change state from connected to disconnected
	select {
	case <-p.flags.disconnected:
		writeDebug("Disconnect successful", 1)
		p.flags.exit = true
		p.close()
		return nil
	case <-time.After(60 * time.Second):
		p.forceDisconnect()
		p.flags.exit = true
		p.close()
		return fmt.Errorf("Disconnect timed out")
	}
	writeDebug("Close() finished.", 1)
	return nil
}

// TxBufferLen returns the number of bytes in the out buffer queue.
//
func (p *Modem) TxBufferLen() int {
	p.mux.bufLen.Lock()
	defer p.mux.bufLen.Unlock()
	writeDebug("TxBufferLen called ("+strconv.Itoa(p.sendBufLen)+" bytes remaining in buffer)", 2)
	return p.sendBufLen + (p.getNumFramesNotTransmitted() * MaxSendData)
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

	writeDebug("write: "+cmd, 2)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	if _, err := p.device.Write([]byte(cmd + "\r")); err != nil {
		writeDebug(err.Error(), 2)
		return err
	}
	time.Sleep(5 * time.Millisecond)
	return nil
}

// Write channel to serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) writeChannel(msg string, ch int, isCommand bool) error {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._writeChannel(msg, ch, isCommand)
}

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
		writeDebug("ERROR in Stuff: "+err.Error(), 1)
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
func checkcrc(s string) bool {
	tochecksum, _ := hex.DecodeString(s[4 : len(s)-3])
	chksum := bits.ReverseBytes16(crc16.ChecksumCCITT([]byte(tochecksum)))
	pksum, _ := hex.DecodeString(s[len(s)-4:])
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
func (p *Modem) _writeChannel(msg string, ch int, isCommand bool) error {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return err
	}

	var d string
	switch {
	case !p.packetcounter && !isCommand:
		d = "00"
	case !p.packetcounter && isCommand:
		d = "01"
	case p.packetcounter && !isCommand:
		d = "80"
	case p.packetcounter && isCommand:
		d = "81"
	}

	c := fmt.Sprintf("%02x", ch)
	l := fmt.Sprintf("%02x", (len(msg) - 1))
	s := hex.EncodeToString([]byte(msg))
	// add crc hostmode addons
	bs, _ := hex.DecodeString(docrc(fmt.Sprintf("%s%s%s%s", c, d, l, s)))
	writeDebug("write channel: \n"+hex.Dump(bs), 2)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	if _, err := p.device.Write(bs); err != nil {
		writeDebug(err.Error(), 2)
		return err
	}

	return nil
}

// Read from serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) read(chunkSize int) (int, string, error) {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._read(chunkSize)
}

// Read from serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) _read(chunkSize int) (int, string, error) {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return 0, "", err
	}

	buf := make([]byte, chunkSize)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	n, err := p.device.Read(buf)
	if err != nil {
		writeDebug("Error received during read: "+err.Error(), 1)
		return 0, "", err
	}

	return n, string(buf[0:n]), nil
}
