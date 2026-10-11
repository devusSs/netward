package tlscert

import (
	"net"
	"time"
)

func NewWithDeps(
	cfg Config,
	now func() time.Time,
	hostname func() (string, error),
	addrs func() ([]net.Addr, error),
) (*Manager, error) {
	return newManager(cfg, deps{now: now, hostname: hostname, addrs: addrs})
}
