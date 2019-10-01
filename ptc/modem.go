package pactor

import (
	"time"
	"net"
	"os"
	"bufio"
	"fmt"
	"encoding/hex"
	"strconv"
	"sync"
	"errors"
	"strings"

	"github.com/tarm/serial"
)

const network = "pactor"

type Addr struct { string }

type cstate struct {
	a   int
	b   int
	c   int
	d   int
	e   int
	f   int
}

type pflags struct {
	exit            bool
	closeCalled     bool
	closed          bool
	disconnected    chan struct{}
	connected       chan struct{}
	closeWriting    chan struct{}
}

type pmux struct {
	device          sync.Mutex
	pactor          sync.Mutex
	write           sync.Mutex
	read            sync.Mutex
	close           sync.Mutex
	bufLen          sync.Mutex
}

type Modem struct {
	devicePath      string

	localAddr       string
	remoteAddr      string

	state           State
	stateOld        State

	device          *serial.Port
	mux             pmux
	wg              sync.WaitGroup
	flags           pflags
	channelState    cstate
	goodChunks      int
	recvBuf         chan []byte
	cmdBuf          chan string
	sendBuf         chan byte
	sendBufLen      int
}

func (p *Modem) SetDeadline(t time.Time) error      { return nil }
func (p *Modem) SetReadDeadline(t time.Time) error  { return nil }
func (p *Modem) SetWriteDeadline(t time.Time) error { return nil }

func (a Addr) Network() string { return network }
func (a Addr) String() string { return a.string }

func (p *Modem) RemoteAddr() net.Addr { return Addr{p.remoteAddr} }
func (p *Modem) LocalAddr() net.Addr  { return Addr{p.localAddr} }


// Initialise the pactor modem and all variables. Switch the modem into hostmode.
// Start the link setup.
//
// Will abort if modem reports failed link setup, Close() is called or timeout
// has occured (90 seconds)
func OpenModem(path string, baudRate int, myCall string, initScript string) (p *Modem, err error) {

	p = &Modem {
		// Initialise variables
		devicePath:      path,

		localAddr:       myCall,
		remoteAddr:      "",

		state:           Unknown,
		stateOld:        Unknown,

		device:          nil,
		flags:           pflags {
						   exit: false,
						   closeCalled: false,
						   disconnected: make(chan struct{}, 1),
						   connected: make(chan struct{}, 1),
						   closeWriting: make(chan struct{}), },
		channelState:    cstate { a: 0, b: 0, c: 0, d:0, e:0, f:0, },
		goodChunks:      0,
		recvBuf:         make(chan []byte, 0),
		cmdBuf:          make(chan string, 0),
		sendBuf:         make(chan byte, MaxSendData),
		sendBufLen:      0,
	}

	writeDebug("Initialising pactor modem", 1)
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return nil, err
	}

	//Setup serial device
	c := &serial.Config{Name: p.devicePath, Baud: baudRate, ReadTimeout: time.Second * SerialTimeout}
	if p.device, err = serial.OpenPort(c); err != nil {
		writeDebug(err.Error(), 1)
		return nil, err
	}

	p.device.Flush()

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
	commands := []string{"MYcall " + p.localAddr, "PTCH " + strconv.Itoa(PactorChannel),
		"MAXE 35", "CM 0", "REM 0", "BOX 0", "MAIL 0", "CHOB 0", "UML 1",
		"TONES 4", "MARK 1600", "SPACE 1400", "CWID 0", "CONType 3", "MODE 0"}

	for _, cmd := range commands {
		var res string
		_, res, err = p.writeAndGetResponse(cmd, -1, false, 1024)
		if err != nil {
			return nil, err
		}
		if strings.Contains(res, "ERROR"){
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

// Call a remote target
//
// BLOCKING until either connected, pactor states disconnect or timeout occures
func (p *Modem) call(targetCall string) (err error){
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
	writeDebug("start status thread", 2)
	for {
		if p.flags.exit {
			break
		}

		p.updatePactorState()
		p.eventHandler()

		time.Sleep(500 * time.Millisecond)
	}
	p.wg.Done()
	writeDebug("exit status thread", 2)
}

// Receive thread: receiving data from modem if available
//
// Pactor modem status must be "connected". New data is written to the recv_buffer
// channel. BLOCKING until recvBuf has been read (e.g. by Read() function)
func (p* Modem) receiveThread() {
	writeDebug("start receive thread", 2)

	for {
		if p.flags.exit {
			close(p.recvBuf)
			break
		}

		if p.state == Connected {
			var res []byte
			chunkSize := 10240

			if channels, err := p.getChannelsWithOutput(); err == nil {
				for _, c := range channels {
					if c == PactorChannel {
						_, data, _ := p.writeAndGetResponse("G", c, true, chunkSize)
						if _, res, _ = p.checkResponse(data, c); res != nil {
							p.recvBuf <- res
							writeDebug("response: " + string(res), 3)
						}
						break
					}
				}
			}
		}

		time.Sleep(1000 * time.Millisecond)
	}
	p.wg.Done()
	writeDebug("exit receive thread", 2)
}

// Send thread: Write data or command into the transmit buffer of the modem
//
// Commands are sent imediately where as payload (e.g. written by Write()) is
// held back until maximum number of frames not transmitted drops below
// MaxFrameNotTX
func (p *Modem) sendThread() {
	writeDebug("start send thread", 2)

	for {
		if p.flags.exit {
			break
		}

		select {
		case cmd := <-p.cmdBuf:
			writeDebug("Write (" + strconv.Itoa(len(cmd)) + "): " + cmd, 2)
			p.writeChannel(cmd, PactorChannel, true)
		default:
		}

		if p.getNumFramesNotTransmitted() < MaxFrameNotTX {
			data := p.getSendData()
			if len(data) > 0 {
				writeDebug("Write (" + strconv.Itoa(len(data)) + "): " + hex.EncodeToString(data), 2)
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
func (p *Modem) getSendData() (data []byte){
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
				writeDebug("No more data to send after " + strconv.Itoa(count) + " bytes", 3)
				return
			}
		} else {
			writeDebug("Reached max send data size (" + strconv.Itoa(count) + " bytes)", 3)
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
	return p.send("D")
}

// Force disconnect
//
// Try to disconnect with disconnect() first. Clears all data remaining in
// modem transmission buffer
func (p *Modem) forceDisconnect() (err error) {
	return p.send("DD")
}

// Send command to the pactor modem
func (p *Modem) send(msg string) error {
	p.cmdBuf <- msg
	return nil
}

// Close current serial connection. Stop all threads and close all channels.
func (p *Modem) close() (err error) {
	if p.flags.closed {
		return nil
	}

	p.flags.closed = true
	p.flags.exit = true
	close(p.flags.closeWriting)
	close(p.flags.disconnected)
	close(p.flags.connected)
	close(p.cmdBuf)
	close(p.sendBuf)

	writeDebug("waiting for all threads to exit", 2)
	p.wg.Wait()

	p.hostmodeQuit()
	p.device.Close()

	return nil
}

// Start modem hostemode (WA8DED)
func (p *Modem) hostmodeStart() error {
	writeDebug("start hostmode", 1)
	_, _, err := p.writeAndGetResponse("JHOST1", -1, false, 1024);
	_, _, err = p.read(1024)
	return err
}

// Quit modem hostmode
func (p *Modem) hostmodeQuit() error {
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
func (p *Modem) eventHandler(){
	if p.state != p.stateOld {
		if p.stateOld == LinkSetup && p.state == Connected {
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
				go p.close() //run as separate thread in order not to block
			}
			setFlag(p.flags.disconnected)
			writeDebug("Disconnect occured", 1)
		}
		if p.stateOld == Connected && p.state == DisconnectReq {
			go p.Close() //run as separate thread in order not to block
			writeDebug("Disconnect requested", 1)
		}

		p.stateOld = p.state
	}
}

// Set specified flag (channel)
func setFlag(flag chan struct{}){
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

	writeDebug("Channels with output: " + fmt.Sprintf("%#v", channels), 3)

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

	writeDebug(fmt.Sprintf("%+v", channelState), 2)

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
	length := int(head[2])+1
	pl := len(payload)
	if int(head[0]) != ch {
		writeDebug("WARNING: Returned data does not match polled channel", 1)
		return 0, nil, fmt.Errorf("Channel missmatch")
	}
	if int(head[1]) != 7 {
		writeDebug("Not a data response: " + string(payload), 1)
		return 0, nil, fmt.Errorf("Not a data response")
	}
	if length != pl {
		writeDebug("WARNING: Data length " + strconv.Itoa(pl) + " does not match stated amount " + strconv.Itoa(length) + ". After " + strconv.Itoa(p.goodChunks) + " good chunks.", 1)
		p.goodChunks = 0
		if pl < length {
			// TODO: search for propper go function
			for i := 0; i < (length-pl); i++ {
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
func (p *Modem) writeAndGetResponse(msg string, ch int, isCommand bool, chunkSize int) (int, string, error){
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
	time.Sleep(100 * time.Millisecond)
	n, str, err := p._read(chunkSize)

	writeDebug("response: " + str, 3)

	return n, str, err
}

// Write to serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) write(cmd string) (error) {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._write(cmd)
}

// Write to serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) _write(cmd string) (error) {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return err
	}

	writeDebug("write: " + cmd, 2)

	p.mux.device.Lock()
	defer p.mux.device.Unlock()
	if _, err := p.device.Write([]byte(cmd + "\r")); err != nil {
		writeDebug(err.Error(), 2)
		return err
	}

	return nil
}

// Write channel to serial connection (thread safe)
//
// No other read/write operation allowed during this time
func (p *Modem) writeChannel(msg string, ch int, isCommand bool) (error) {
	p.mux.pactor.Lock()
	defer p.mux.pactor.Unlock()
	return p._writeChannel(msg, ch, isCommand)
}

// Write channel to serial connection (NOT thread safe)
//
// If used, make shure to lock/unlock p.mux.pactor mutex!
func (p *Modem) _writeChannel(msg string, ch int, isCommand bool) (error) {
	if err := p.checkSerialDevice(); err != nil {
		writeDebug(err.Error(), 1)
		return err
	}

	// command => 01
	d := "01"
	if !isCommand {
		// info/data => 00
		d = "00"
	}

	c := fmt.Sprintf("%02x", ch)
	l := fmt.Sprintf("%02x", (len(msg)-1))
	s := hex.EncodeToString([]byte(msg))
	bs, _ := hex.DecodeString(fmt.Sprintf("%s%s%s%s", c, d, l, s))

	writeDebug("write channel: " + string(bs), 3)

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
	if  err != nil {
		writeDebug(err.Error(), 2)
		return 0, "", err
	}

	return n, string(buf[0:n]), nil
}
