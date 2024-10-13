package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go"
	"github.com/cromefire/fritzbox-cloudflare-dyndns/pkg/logging"
	"golang.org/x/net/publicsuffix"
)

type Action struct {
	DnsRecord string
	CfZoneId  string
	IpVersion int
}

type CloudFlareConfigs struct {
	token string

	email string
	key   string

	retryPolicy string
}

type CloudflareUpdater struct {
	options *UpdaterOptions
	configs *CloudFlareConfigs

	actions []*Action

	isInit bool
	In     chan *net.IP
	log    *slog.Logger

	api *cf.API
}

func (updater *CloudflareUpdater) OnNewIp(ip *net.IP) {
	updater.In <- ip
}

func NewCLoudflareConfigs(token string, email string, key string, retryPolicy string) *CloudFlareConfigs {
	return &CloudFlareConfigs{
		token:       token,
		email:       email,
		key:         key,
		retryPolicy: retryPolicy,
	}
}

func NewCloudflareUpdater(options *UpdaterOptions, configs *CloudFlareConfigs, log *slog.Logger) (Updater, error) {
	updater := &CloudflareUpdater{
		isInit:  false,
		In:      make(chan *net.IP, 10),
		log:     log.With(slog.String("updater", "cloudflare")),
		options: options,
		configs: configs,
	}

	err := updater.InitApi()

	if err != nil {
		return nil, err
	}

	err = updater.init()

	if err != nil {
		return nil, err
	}

	updater.StartWorker()

	return updater, err
}

func (u *CloudflareUpdater) InitApi() error {
	var api *cf.API
	var err error
	if u.configs.token == "" {
		api, err = cf.New(u.configs.key, u.configs.email)
	} else {
		api, err = cf.NewWithAPIToken(u.configs.token)
	}

	if err != nil {
		return err
	}

	u.api = api

	if u.configs.retryPolicy != "" {

		retryPolicySplit := strings.Split(u.configs.retryPolicy, " ")

		var maxRetries, minRetryDelaySeconds, maxRetryDelaySecs int
		maxRetries, err = strconv.Atoi(retryPolicySplit[0])
		if err != nil {
			return errors.New("Failed to parse retry policy's maxRetries: " + err.Error())
		}

		minRetryDelaySeconds, err = strconv.Atoi(retryPolicySplit[1])
		if err != nil {
			return errors.New("Failed to parse retry policy's minRetryDelaySeconds: " + err.Error())
		}

		maxRetryDelaySecs, err = strconv.Atoi(retryPolicySplit[2])

		if err != nil {
			return errors.New("Failed to parse retry policy's maxRetryDelaySecs: " + err.Error())
		}

		u.log.Info(fmt.Sprintf("Setting Cloudflare retry policy. MaxRetries %d, minRetryDelaySeconds %ds, maxRetryDelaySeconds %ds.",
			maxRetries, minRetryDelaySeconds, maxRetryDelaySecs))
		cf.UsingRetryPolicy(maxRetries, minRetryDelaySeconds, maxRetryDelaySecs)(api)
	}
	return nil
}

func (u *CloudflareUpdater) init() error {
	// Create unique list of zones and fetch their Cloudflare zone IDs

	zoneIdMap := make(map[string]string)

	for _, val := range u.options.ipv4Zones {
		zoneIdMap[val] = ""
	}

	for _, val := range u.options.ipv6Zones {
		zoneIdMap[val] = ""
	}

	for val := range zoneIdMap {
		zone, err := publicsuffix.EffectiveTLDPlusOne(val)

		if err != nil {
			return err
		}

		id, err := u.api.ZoneIDByName(zone)

		if err != nil {
			return err
		}

		zoneIdMap[val] = id
	}

	// Now create an updater action list
	for _, val := range u.options.ipv4Zones {
		a := &Action{
			DnsRecord: val,
			CfZoneId:  zoneIdMap[val],
			IpVersion: 4,
		}

		u.actions = append(u.actions, a)
	}

	for _, val := range u.options.ipv6Zones {
		a := &Action{
			DnsRecord: val,
			CfZoneId:  zoneIdMap[val],
			IpVersion: 6,
		}

		u.actions = append(u.actions, a)
	}

	u.isInit = true

	return nil
}

func (u *CloudflareUpdater) StartWorker() {
	if !u.isInit {
		return
	}

	go u.spawnWorker()
}

func (u *CloudflareUpdater) spawnWorker() {
	for {
		select {
		case ip := <-u.In:
			if ip.To4() == nil {
				if u.options.lastIpv6 != nil && u.options.lastIpv6.Equal(*ip) {
					continue
				}
			} else {
				if u.options.lastIpv4 != nil && u.options.lastIpv4.Equal(*ip) {
					continue
				}
			}
			u.log.Info("Received update request", slog.Any("ip", ip))

			for _, action := range u.actions {
				// Skip IPv6 action mismatching IP version
				if ip.To4() == nil && action.IpVersion != 6 {
					continue
				}

				// Skip IPv4 action mismatching IP version
				if ip.To4() != nil && action.IpVersion == 6 {
					continue
				}

				// Create detailed sub-logger for this action
				alog := u.log.With(slog.String("domain", fmt.Sprintf("%s/IPv%d", action.DnsRecord, action.IpVersion)))

				// Decide record type on ip version
				var recordType string

				if ip.To4() == nil {
					recordType = "AAAA"
				} else {
					recordType = "A"
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)

				rc := cf.ZoneIdentifier(action.CfZoneId)

				// Research all current records matching the current scheme
				records, _, err := u.api.ListDNSRecords(ctx, rc, cf.ListDNSRecordsParams{
					Type: recordType,
					Name: action.DnsRecord,
				})

				if err != nil {
					alog.Error("Action failed, could not research DNS records", logging.ErrorAttr(err))
					os.Exit(1)
					continue
				}

				// Create record if none were found
				if len(records) == 0 {
					alog.Info("Creating DNS record")

					proxied := false

					_, err := u.api.CreateDNSRecord(ctx, rc, cf.CreateDNSRecordParams{
						Type:    recordType,
						Name:    action.DnsRecord,
						Content: ip.String(),
						Proxied: &proxied,
						TTL:     120,
						ZoneID:  action.CfZoneId,
					})

					if err != nil {
						alog.Error("Action failed, could not create DNS record", logging.ErrorAttr(err))
						os.Exit(1)
						continue
					}
				}

				// Update existing records
				for _, record := range records {
					alog.Info("Updating DNS record", slog.Any("record-id", record.ID))

					if record.Content == ip.String() {
						continue
					}

					// Ensure we submit all required fields even if they did not change,otherwise
					// cloudflare-go might revert them to default values.
					_, err := u.api.UpdateDNSRecord(ctx, rc, cf.UpdateDNSRecordParams{
						ID:      record.ID,
						Content: ip.String(),
						TTL:     record.TTL,
						Proxied: record.Proxied,
					})

					if err != nil {
						alog.Error("Action failed, could not update DNS record", logging.ErrorAttr(err))
						os.Exit(1)
						continue
					}
				}

				cancel()
			}

			if ip.To4() == nil {
				u.options.lastIpv6 = ip
			} else {
				u.options.lastIpv4 = ip
			}
		}
	}
}
