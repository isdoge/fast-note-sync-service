package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
)

// gitRepoLookupIP is variable for deterministic DNS tests.
var gitRepoLookupIP = net.DefaultResolver.LookupIP

var gitRepoHTTPClientsMu sync.Mutex

var errGitRepoPrivateNetwork = errors.New("private network access not allowed for repository URL")

type gitRepoURLValidator struct {
	allowedPrivateHosts []string
	lookupIP            func(context.Context, string, string) ([]net.IP, error)
}

func newGitRepoURLValidator(allowedPrivateHosts []string, lookupIP func(context.Context, string, string) ([]net.IP, error)) *gitRepoURLValidator {
	if lookupIP == nil {
		lookupIP = gitRepoLookupIP
	}
	return &gitRepoURLValidator{allowedPrivateHosts: allowedPrivateHosts, lookupIP: lookupIP}
}

// validateRepoURL validates git repository URL format and protocols to prevent file:// and SSRF.
// Private addresses are allowed only when both the configured host rule and resolved IPs match.
// validateRepoURL 校验 git 仓库 URL 格式和协议，防止 file:// 及 SSRF；白名单同时约束主机名和 DNS 解析结果。
func (v *gitRepoURLValidator) validateRepoURL(rawURL string) error {
	if rawURL == "" {
		return code.ErrorGitSyncValidateFailed.WithDetails("repository URL is empty")
	}
	if !strings.Contains(rawURL, "://") && !(strings.Contains(rawURL, "@") && strings.Contains(rawURL, ":")) {
		// Local paths are not network requests and are already supported by existing sync tests.
		return nil
	}

	if strings.Contains(rawURL, "@") && strings.Contains(rawURL, ":") && !strings.Contains(rawURL, "://") {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return code.ErrorGitSyncValidateFailed.WithDetails("invalid repository URL format")
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		return code.ErrorGitSyncValidateFailed.WithDetails("repository URL must specify protocol (e.g., https/ssh)")
	}
	if scheme != "http" && scheme != "https" && scheme != "ssh" && scheme != "git" {
		return code.ErrorGitSyncValidateFailed.WithDetails("unsupported git protocol: " + scheme)
	}
	if scheme != "http" && scheme != "https" {
		return nil
	}

	host := u.Hostname()
	if host == "" {
		return code.ErrorGitSyncValidateFailed.WithDetails("invalid hostname in repository URL")
	}
	if err := v.validateResolvedIPs(host, nil); err != nil {
		return code.ErrorGitSyncValidateFailed.WithDetails(err.Error())
	}
	return nil
}

// validateResolvedIPs checks every private resolved IP against the allowlist. With hostname
// rules, the hostname itself must also be listed, so a broad CIDR cannot authorize arbitrary
// public-to-private DNS rebinding. Any private IP outside a configured CIDR is rejected.
// validateResolvedIPs 检查每个私网解析地址；主机名规则必须同时匹配，CIDR 不能替任意主机名放行。
func (v *gitRepoURLValidator) validateResolvedIPs(host string, resolvedIPs []net.IP) error {
	host = normalizeGitRepoHost(host)
	ip := net.ParseIP(host)
	if ip == nil && strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		resolvedIPs = []net.IP{net.ParseIP("127.0.0.1")}
	} else if ip == nil && resolvedIPs == nil {
		ips, err := v.lookupIP(context.Background(), "ip", host)
		if err != nil {
			if len(v.allowedPrivateHosts) > 0 {
				return fmt.Errorf("resolve repository hostname %s: %w", host, err)
			}
			// Preserve the existing behavior: connection errors are reported by go-git.
			return nil
		}
		resolvedIPs = ips
	} else if ip != nil {
		resolvedIPs = []net.IP{ip}
	}

	if len(v.allowedPrivateHosts) == 0 {
		for _, resolvedIP := range resolvedIPs {
			if isPrivateOrLocalIP(resolvedIP) {
				return errGitRepoPrivateNetwork
			}
		}
		return nil
	}

	hostnameRule := v.hostnameAllowed(host)
	if hostnameRule && ip == nil && len(resolvedIPs) == 0 {
		return errGitRepoPrivateNetwork
	}
	if ip == nil && hostnameRule {
		for _, resolvedIP := range resolvedIPs {
			if !isPrivateOrLocalIP(resolvedIP) || !v.privateIPAllowed(resolvedIP) {
				return errGitRepoPrivateNetwork
			}
		}
		return nil
	}
	for _, resolvedIP := range resolvedIPs {
		if isPrivateOrLocalIP(resolvedIP) && !v.privateIPAllowed(resolvedIP) {
			return errGitRepoPrivateNetwork
		}
	}
	return nil
}

func (v *gitRepoURLValidator) hostnameAllowed(host string) bool {
	for _, entry := range v.allowedPrivateHosts {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil || net.ParseIP(entry) != nil {
			continue
		}
		if normalizeGitRepoHost(entry) == host {
			return true
		}
	}
	return false
}

func (v *gitRepoURLValidator) privateIPAllowed(ip net.IP) bool {
	for _, entry := range v.allowedPrivateHosts {
		entry = strings.TrimSpace(entry)
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
			return true
		}
		if allowedIP := net.ParseIP(entry); allowedIP != nil && allowedIP.Equal(ip) {
			return true
		}
	}
	return false
}

func normalizeGitRepoHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// installHTTPClients serializes go-git's package-level HTTP(S) client selection. Every actual
// dial is checked again because go-git resolves DNS independently of URL validation and follows
// redirects. The previous clients are restored after the Git operation.
// installHTTPClients 串行覆盖 go-git 的全局 HTTP(S) 客户端；实际拨号再次校验 DNS 与重定向目标。
func (v *gitRepoURLValidator) installHTTPClients() func() {
	gitRepoHTTPClientsMu.Lock()
	previousHTTP := gitclient.Protocols["http"]
	previousHTTPS := gitclient.Protocols["https"]

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = v.dialContext(transport.DialContext)
	transport.DialTLSContext = nil
	transport.DialTLS = nil
	client := githttp.NewClient(&http.Client{Transport: transport})
	gitclient.InstallProtocol("http", client)
	gitclient.InstallProtocol("https", client)

	return func() {
		gitclient.InstallProtocol("http", previousHTTP)
		gitclient.InstallProtocol("https", previousHTTPS)
		gitRepoHTTPClientsMu.Unlock()
	}
}

func (v *gitRepoURLValidator) dialContext(base func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if base == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		base = dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := v.lookupIP(ctx, network, host)
		if err != nil {
			return nil, err
		}
		if err := v.validateResolvedIPs(host, ips); err != nil {
			return nil, err
		}
		if net.ParseIP(host) == nil && len(ips) > 0 {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			address = net.JoinHostPort(ips[0].String(), port)
		}
		return base(ctx, network, address)
	}
}

func (s *gitSyncService) repoURLValidator() *gitRepoURLValidator {
	var allowed []string
	if s.gitConf != nil {
		allowed = s.gitConf.AllowedPrivateHosts
	}
	return newGitRepoURLValidator(allowed, nil)
}

func installGitRepoLookupForTest(lookup func(context.Context, string, string) ([]net.IP, error)) func() {
	previous := gitRepoLookupIP
	gitRepoLookupIP = lookup
	return func() { gitRepoLookupIP = previous }
}
