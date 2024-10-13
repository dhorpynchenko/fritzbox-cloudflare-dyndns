package updater

import (
	"log/slog"
	"net"
)

type NoOPUpdater struct {
	options *UpdaterOptions
	log     *slog.Logger
}

func (updater *NoOPUpdater) OnNewIp(ip *net.IP) {
	updater.log.Debug("NoOPUpdater Received new IP " + ip.String())
}

func NewNoOPUpdater(options *UpdaterOptions, log *slog.Logger) Updater {
	return &NoOPUpdater{
		options: options,
		log:     log.With(slog.String("updater", "noop")),
	}
}
