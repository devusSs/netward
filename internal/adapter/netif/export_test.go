package netif

import "net"

type Interface = iface

func NewInterface(name string, flags net.Flags, addrs ...net.Addr) Interface {
	return iface{name: name, flags: flags, addrs: addrs}
}

func (r *Reader) SetInterfaces(interfaces func() ([]Interface, error)) {
	r.interfaces = interfaces
}
