# ptc-go - SCS PACTOR modems driver for the Pat Winlink-client

(If you came here through the Pat Wiki, please read the full text!)

## What is this?

The code in this repository is a plug-in (you may also call it a
"driver") to support PACTOR modems manufacted by SCS in the
[Pat Winlink-client](http://getpat.io/). It does this by communicating
with the PACTOR modems using the WA8DED hostmode, enabling full binary transparency.

Note that this is **work in progress**, things may or may not work
properly. Use it at your own risk. The code is not feature complete yet. It should
be usable enough to send Winlink messages through a PACTOR (or PACKET)
channel using a supported PACTOR modem, though.

The code in this repository is independently developed from Pat
itself, although in close collaboration. Please do not bother Martin, LA5NTA, with
questions concerning this driver. Instead, look [further down](https://github.com/harenber/ptc-go/blob/master/README.md#seeking-help).

Author: Torsten Harenberg, DL1THM (initial) with large contributions and bug fixes from @blockmurder. The code in the "develop" branch has been written by @blockmurder.

## How to use it

### setup

#### How to get it


The PACTOR support is beta and included in the standard distribution of [Pat](http://getpat.io) since v0.7.0.

#### setup

Once you have successfully compiled Pat as described above, [configure it](https://github.com/la5nta/pat/wiki/The-command-line-interface#configure). Afterwards you should add an entry to your $HOME/.wl2k/config.json file like this:

```json
"pactor": {
  "path": "/dev/ttyUSB0",
  "baudrate": 57600,
  "rig": "",
  "custom_init_script": "/home/pi/ptcinit.txt"
},
```

Path is the tty to your SCS modem. The example here is from Linux and I haven't tested this on any other platform yet.
This __custom_init_script__ parameter is completely optional and may
contain commands which are send to the modem before switching it to
WA8DED-mode. It is generally not needed! Examples of commands which
could be useful are setting the audio level or any command needed to
select the right frequency, if you steer your radio through the
modem. Please check the modem's manual for details. You **do not**
need to set basic parameters like your callsign with this
feature. This is handled by the code.


### connect

To connect to the remote station PJ2A using the defaults set in your
config.json file

```
pat connect pactor:///PJ2A
```

~~To overwrite one or the other default in the config.json, you may use the following additional parameters~~ deprecated

```
pat connect "pactor:///PJ2A?host=/dev/ttyUSB0&baud=57600"
```

(new feature introduced in v2.1) In addition to __custom_init_script__, you can set
additional commands to the PTC on the commandline with the init parameter. For example:

```
pat connect "pactor:///PJ2A?init=PTCH%2031"
```

To prevent the shell to interpret any character, I highly recommand you 
put the URL into quotation marks. Remember: these commands are sent to your
PTC. Depending on what they do, this changed the behaviour of your modem
also in future connectios. Refer to the SCS handbook for details.

A successful connect looks like this:

```
pi@pi2 ~/gopackages/src/github.com/la5nta/pat $ ./pat connect pactor:///PJ2A
2018/04/22 13:16:21 Connecting to PJ2A (pactor)...
2018/04/22 13:16:23 Connected to PJ2A ()
PJ2A - Linux RMS Gateway 2.4.0 Oct 24 2017 (FK52nd)

Welcome to the PJ2A Winlink 2000 RMS Gateway. VERONA Radio Club, Curacao, Dutch Caribbean

INFO: Host Name sandiego.winlink.org, Port 8772
Connected
[WL2K-5.0-B2FWIHJM$]
;PQ: ABCDEFGH
CMS via PJ2A >
>FF
;PM: DL1THM P7TVSXXASKRJ 987 SERVICE@winlink.org //WL2K User Notice
FC EM P7TVSXXASKRJ 1873 987 0
F> DE
1 proposal(s) received
Accepting P7TVSXXASKRJ
Receiving [//WL2K User Notice] [offset 0]
//WL2K User Notice: 100%
>FF
FQ
```

#### using Packet radio

(for advanced users only)

You can change the PTC to use PACKET radio instead of PACTOR by moving
the PACTOR channel away from the default channel 31 (Channel is a data
stream inside the WA8DED protocol). The easiest way to do that is to
add a init option to the connect command like for example:

```
./pat connect "pactor:///PJ2A?init=PTCH%204"
```


## Supported hardware

The code has tested against the PTC-II and -III series of the SCS PTC
modems. It should work with USB, serial and Bluetooth connections.
v2.2.0 brings (finally) support for the Dragon PACTOR-4 series of
modems. 

Do get that supported, v2.2.0 changes quite a bit:

- the driver speaks now the enhanced CRC Hostmode instead of plain WA8DED hostmode to the modem,
- the underlying serial library has changed to [this one](https://github.com/albenik/go-serial) supporting  non-standard baud rates.

The P4 Dragon series use a non-standard baud rate of 829440 baud, so an example config.json for Linux is

```
  "pactor": {
    "path": "/dev/ttyUSB0",
    "baudrate": 829440,
    "rig": "",
    },
```

For operating systems other than Linux (MacOS and Windows), you need to install the SCS driver and follow their instructions about the baudrate to use.

Support of the Dragon modems is new and has been followed in [issue #3](https://github.com/harenber/ptc-go/issues/3).
Swiss PTC modems should be supported as well, see [issue #26](https://github.com/harenber/ptc-go/issues/26).

## What is missing

There are a lot of features that would be nice to have and which are
still under development. Most notably these are:

* listen mode. At the moment, the driver can only call remote station but cannot accept connections.
* Rig contol through the PTC.

If you feel you are able to contribute, you are more than
welcome. Please comment on the appropreate issues.

Furthermore, tests on non-Linux systems would be appreciated.

## Debugging

Setting an environment variable `pactor_debug=1` will enable some more
debug messages. You can set the verbosity level from 1 (little debug output) to 3 (a LOT of debug output).

## Seeking help

The best place to ask for help is the
[Pat Google Group](https://groups.google.com/forum/#!forum/pat-users).

That is true even if you have questions concerning this driver, I (DL1THM) monitor the Pat Google Group as well and will answer there.

Otherwise, feel free to open issues to this repository if you find bugs not reported yet.

## Acknowledments

First I wish to thank Martin Hebnes Pedersen, LA5NTA, for developing
Pat, his patience and for his helpful code reviews. Further thanks to my good friend
Brett Ruiz, PJ2BR, for providing a second station for beta-testing. And thanks to @blockmurder for
the testing and patches. Futhermore, I would like to thank the nice folks at SCS for 
their support.
