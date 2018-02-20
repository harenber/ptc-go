# ptc-go - SCS PACTOR modems driver for the Pat Winlink-client

## What is this?

The code in this repository is a plug-in (you may also call it a
"driver") to support PACTOR modems manufacted by SCS in the
[Pat Winlink-client](http://getpat.io/). It does this by communication
with the PACTOR modems using the WA8DED hostmode, enabling full binary transparency.

Note that this is **work in progress**, things may or may not work
properly. Use it at your own risk. The code is not feature complete yet. It should
be usable enough to send Winlink messages through a PACTOR (or PACKET)
channel using a supported PACTOR modems, though.

The code in this repository is independently developed from Pat
itself, although in close collaboration.

Author: Torsten Harenberg, DL1THM

## How to use it

### setup

For the time being, you need to compile Pat manually with these
sources included. Instructions for this are still to be written. So
this code may only suitable for experiences users.

Furthermore, you .wl2k/config.json file should contain an entry like this:

```json
	"ptc": {
	"path": "/dev/ttyUSB0",
	"rig": "",
	"custom_init_script": "/home/pi/ptcinit.txt"
	},
```

This __custom_init_script__ parameter is completely optional and may
contain commands which are send to the modem before switching it to
WA8DED-mode. It is generally not needed! Examples of commands which
could be useful are setting the audio level or any command needed to
select the right frequency, if you steer your radio through the
modem. Please check the modem's manual for details. You **do not**
need to set basic parameters like your callsign with this
feature. This is handled by the code anyhow.

### connect

To connect to the remote station PJ2A using the defaults set in your
config.json file

```
pat connect ptc:///PJ2A
```

To overwrite one or the other default in the config.json, you may use

```
pat connect "ptc:///PJ2A?host=/dev/ttyUSB0&baud=57600"
```

#### using Packet radio

(for advanced users only)

You can change the PTC to use PACKET radio instead of PACTOR by moving
the PACTOR channel away from the default channel 4 (Channel is a data
stream inside the WA8DED protocol). The easiest way to do that is to
add a

```
PTCH 1
```

to the script which is called by `custom_init_script`. 

## Supported hardware

The code has tested against the PTC-II and -III series of the SCS PTC
modems. It should work with USB, serial and Bluetooth connections. The
P4 Dragon seems to use a non-standard baudrate on the serial line,
which is not supported by the underlying Go package. So for the time
being, these new modems are unfortunately **not** supported. If you
think you can contribute, please feel free to comment on https://github.com/harenber/ptc-go/issues/3

## What is missing

There are a lot of features that would be nice to have and which are
still under development. Most notably these are:

* listen mode. At the moment, the driver can only call remote station
but cannot accept connections. 
* Get rid of interrupt handling inside the driver.
* P4 Dragon support.

If you feel you are able to contribute, you are more than
welcome. Please comment on the appropreate issues.

Furthermore, tests on non-Linux systems would be appreciated.

## Debugging

Setting an environment variable `ptc_debug` will enable some more
debug messages. This feature will become more verbose in the future
and will probably include the data transferred from and to the PACTOR modem.

## Seeking help

The best place to ask for help is the
[Pat Google Group](https://groups.google.com/forum/#!forum/pat-users).

## Acknowledments

First I wish to thank Martin Hebnes Pedersen, LA5NTA, for developing
Pat, his patience and for his helpful code reviews. Further thanks to
Brett Ruiz, PJ2BR, for providing a second station for beta-testing.
