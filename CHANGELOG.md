# Changelog

* Quite huge rewrite of large parts of the code to enable listen mode on PACTOR
* Try to leave CRC-Hostmode before we start to talk to the modem
* CALLs to remote station can now be interrupted, implemented `DialURLContext` for this
* bump albernik/go-serial to 2.5.1

## v2.2.3

* bump albenik/go-serial to v2.5.0, enabling `GOOS=android` support, see [albenik/go-serial/#29](https://github.com/albenik/go-serial/issues/29)
* modem.go: set current DATE and TIME on the PACTOR modem during modem init.
