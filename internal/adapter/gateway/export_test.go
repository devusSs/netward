package gateway

import "net"

type Interface = iface

func NewInterface(name string, addrs ...net.Addr) Interface {
	return iface{name: name, addrs: addrs}
}

func NewWith(
	discoverIPv4, discoverIPv6, discoverIfaceIPv6 func() (net.IP, error),
	interfaces func() ([]Interface, error),
) *Finder {
	return &Finder{
		discoverIPv4:      discoverIPv4,
		discoverIPv6:      discoverIPv6,
		discoverIfaceIPv6: discoverIfaceIPv6,
		interfaces:        interfaces,
	}
}
