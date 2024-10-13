package updater

import (
	"net"
)

type UpdaterOptions struct {
	ipv4Zones []string
	ipv6Zones []string

	lastIpv4 *net.IP
	lastIpv6 *net.IP
}

type Updater interface {
	OnNewIp(ip *net.IP)
}

func NewUpdaterOptions(ipv4Zones []string, ipv6Zones []string) *UpdaterOptions {
	return &UpdaterOptions{
		ipv4Zones: ipv4Zones,
		ipv6Zones: ipv6Zones,
	}
}
