package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var blockedHostSuffixes = []string{
	".localhost",
	".local",
	".internal",
	".lan",
	".home.arpa",
	".cluster.local",
}

func validateAgentURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() {
		return errors.New("agent_card.url must be a valid absolute http/https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("agent_card.url must use http or https")
	}
	if parsed.User != nil {
		return errors.New("agent_card.url must not include user info")
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return errors.New("agent_card.url host is required")
	}
	if isBlockedHostname(host) {
		return fmt.Errorf("agent_card.url host is not allowed: %s", host)
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		if !isAllowedIP(ip) {
			return fmt.Errorf("agent_card.url host is not allowed: %s", host)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host); err == nil {
		for _, ip := range ips {
			if !isAllowedIP(ip) {
				return fmt.Errorf("agent_card.url host resolves to a private address: %s", host)
			}
		}
	}

	return nil
}

func isBlockedHostname(host string) bool {
	if host == "localhost" {
		return true
	}
	for _, suffix := range blockedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func isAllowedIP(ip netip.Addr) bool {
	return ip.IsValid() &&
		ip.IsGlobalUnicast() &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}
