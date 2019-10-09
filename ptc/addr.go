package pactor

const network = "pactor"

type Addr struct { string }

func (a Addr) Network() string { return network }
func (a Addr) String() string { return a.string }
