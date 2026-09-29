package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"echoo/internal/netguard"
)

const lookupTimeout = 5 * time.Second

var errInternalHost = errors.New("host resolves to an internal address")

// normalizeHost accepts a hostname or an IP literal, with optional brackets around IPv6, and
// rejects everything else: schemes, paths, ports, credentials and whitespace.
func normalizeHost(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if inner, ok := strings.CutPrefix(s, "["); ok {
		inner, ok = strings.CutSuffix(inner, "]")
		if !ok {
			return "", false
		}
		addr, err := netip.ParseAddr(inner)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", false
		}
		return addr.String(), true
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		if addr.Zone() != "" {
			return "", false
		}
		return addr.String(), true
	}
	if len(s) == 0 || len(s) > 253 {
		return "", false
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	// A numeric last label would let odd IPv4 spellings such as 2130706433 pass as a hostname.
	last := labels[len(labels)-1]
	return s, strings.Trim(last, "0123456789") != ""
}

func resolveHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return addrs, nil
}

// checkHostAtSave reports whether host points at an internal address. A name that does not
// resolve is accepted: it cannot be internal, and the connection test reports it.
func checkHostAtSave(ctx context.Context, host string) (internal bool) {
	addrs, err := resolveHost(ctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if netguard.IsInternal(a) {
			return true
		}
	}
	return false
}

// dialTargets resolves host and refuses internal addresses unless allowed. The caller connects
// to the returned IPs instead of the name, so DNS cannot change the answer between check and
// use.
func dialTargets(ctx context.Context, host string, allowInternal bool) ([]string, error) {
	addrs, err := resolveHost(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]string, len(addrs))
	for i, a := range addrs {
		if !allowInternal && netguard.IsInternal(a) {
			return nil, errInternalHost
		}
		ips[i] = a.Unmap().String()
	}
	return ips, nil
}
