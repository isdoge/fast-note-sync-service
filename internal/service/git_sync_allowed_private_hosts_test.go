package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	appconfig "github.com/haierkeys/fast-note-sync-service/internal/config"
	"github.com/haierkeys/fast-note-sync-service/internal/dto"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type gitRepoLookupFunc func(context.Context, string, string) ([]net.IP, error)

func gitRepoTestLookup(ips map[string][]string) gitRepoLookupFunc {
	return func(_ context.Context, _, host string) ([]net.IP, error) {
		values, ok := ips[host]
		if !ok {
			return nil, fmt.Errorf("unexpected DNS lookup for %s", host)
		}
		result := make([]net.IP, 0, len(values))
		for _, value := range values {
			result = append(result, net.ParseIP(value))
		}
		return result, nil
	}
}

func TestGitSyncAllowedPrivateHostsValidation(t *testing.T) {
	loopback := gitRepoTestLookup(map[string][]string{"gitea.internal": {"127.0.0.1"}})
	private := gitRepoTestLookup(map[string][]string{"gitea.internal": {"10.20.30.40"}})

	for _, tc := range []struct {
		name      string
		rawURL    string
		allowlist []string
		lookup    gitRepoLookupFunc
		wantAllow bool
	}{
		{name: "default rejects private IP", rawURL: "http://192.168.1.20/repo.git", wantAllow: false},
		{name: "exact private IP allowed", rawURL: "http://192.168.1.20/repo.git", allowlist: []string{"192.168.1.20"}, wantAllow: true},
		{name: "CIDR allows private IP", rawURL: "http://192.168.1.20/repo.git", allowlist: []string{"192.168.1.0/24"}, wantAllow: true},
		{name: "other private IP rejected", rawURL: "http://192.168.2.20/repo.git", allowlist: []string{"192.168.1.0/24"}, wantAllow: false},
		{name: "hostname private DNS allowed", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"gitea.internal", "10.20.0.0/16"}, lookup: private, wantAllow: true},
		{name: "hostname loopback DNS allowed", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"GITEA.INTERNAL.", "127.0.0.1"}, lookup: loopback, wantAllow: true},
		{name: "hostname without private result rejected", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"gitea.internal"}, lookup: gitRepoTestLookup(map[string][]string{"gitea.internal": {"93.184.216.34"}}), wantAllow: false},
		{name: "hostname rebinding to other private IP rejected", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"gitea.internal", "10.0.0.0/8"}, lookup: gitRepoTestLookup(map[string][]string{"gitea.internal": {"192.168.1.20"}}), wantAllow: false},
		{name: "CIDR still resolves hostname", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"10.20.0.0/16"}, lookup: private, wantAllow: true},
		{name: "CIDR rejects hostname outside range", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"10.20.0.0/16"}, lookup: gitRepoTestLookup(map[string][]string{"gitea.internal": {"10.30.0.1"}}), wantAllow: false},
		{name: "invalid entry fails closed", rawURL: "http://192.168.1.20/repo.git", allowlist: []string{"not a host"}, wantAllow: false},
		{name: "localhost requires whitelist", rawURL: "http://localhost/repo.git", wantAllow: false},
		{name: "public hostname remains allowed", rawURL: "https://example.com/repo.git", lookup: gitRepoTestLookup(map[string][]string{"example.com": {"93.184.216.34"}}), wantAllow: true},
		{name: "DNS failure with whitelist fails closed", rawURL: "http://gitea.internal/repo.git", allowlist: []string{"gitea.internal", "10.20.0.0/16"}, lookup: gitRepoTestLookup(nil), wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator := newGitRepoURLValidator(tc.allowlist, tc.lookup)
			err := validator.validateRepoURL(tc.rawURL)
			if tc.wantAllow {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGitSyncAllowedPrivateHostsConnectionValidation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n00000000"))
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(serverURL.Port())
	require.NoError(t, err)

	for _, tc := range []struct {
		name       string
		allowlist  []string
		resolvedIP string
		wantAllow  bool
	}{
		{name: "loopback whitelist permits real List", allowlist: []string{"gitea.internal", "127.0.0.1"}, resolvedIP: "127.0.0.1", wantAllow: true},
		{name: "hostname without IP rule rejected at save", allowlist: []string{"gitea.internal"}, resolvedIP: "127.0.0.2", wantAllow: false},
		{name: "rebinding to unlisted private IP blocked before request", allowlist: []string{"gitea.internal", "127.0.0.1", "127.0.0.2"}, resolvedIP: "127.0.0.2", wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests = 0
			lookup := gitRepoTestLookup(map[string][]string{"gitea.internal": {tc.resolvedIP}})
			validator := newGitRepoURLValidator(tc.allowlist, lookup)
			rawURL := fmt.Sprintf("http://gitea.internal:%d/repo.git", port)
			err := validator.validateRepoURL(rawURL)
			if len(tc.allowlist) == 1 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			rem := git.NewRemote(nil, &config.RemoteConfig{Name: "origin", URLs: []string{rawURL}})
			restore := validator.installHTTPClients()
			_, listErr := rem.List(&git.ListOptions{})
			restore()
			if tc.wantAllow {
				require.ErrorIs(t, listErr, transport.ErrEmptyRemoteRepository)
				require.Positive(t, requests)
			} else {
				require.Error(t, listErr)
				require.Zero(t, requests)
			}
		})
	}
}

func TestGitSyncAllowedPrivateHostsDialChecksSecondResolution(t *testing.T) {
	lookups := 0
	lookup := gitRepoLookupFunc(func(_ context.Context, _, host string) ([]net.IP, error) {
		lookups++
		if lookups == 1 {
			return []net.IP{net.ParseIP("192.168.1.20")}, nil
		}
		return []net.IP{net.ParseIP("10.0.0.20")}, nil
	})
	validator := newGitRepoURLValidator([]string{"gitea.internal", "192.168.1.20"}, lookup)
	require.NoError(t, validator.validateRepoURL("http://gitea.internal/repo.git"))

	dial := validator.dialContext(nil)
	_, err := dial(context.Background(), "tcp", "gitea.internal:3000")
	require.Error(t, err)
	require.Contains(t, err.Error(), "private network")
	require.Equal(t, 2, lookups)
}

func TestGitSyncServiceUsesAllowedPrivateHostsForSaveAndConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n00000000"))
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	lookup := gitRepoTestLookup(map[string][]string{"gitea.internal": {"127.0.0.1"}})
	restore := installGitRepoLookupForTest(lookup)
	defer restore()

	svc := &gitSyncService{gitConf: &appconfig.GitConfig{AllowedPrivateHosts: []string{"gitea.internal", "127.0.0.1"}}, logger: zap.NewNop()}
	_, err = svc.UpdateConfig(context.Background(), 1, &dto.GitSyncConfigRequest{RepoURL: "http://192.168.1.20/repo.git"})
	require.Error(t, err)
	require.Equal(t, code.ErrorGitSyncValidateFailed.Code(), tokenErrorCode(t, err))

	svc.gitConf.AllowedPrivateHosts = nil
	err = svc.Validate(context.Background(), &dto.GitSyncValidateRequest{RepoURL: serverURL.Scheme + "://gitea.internal:" + serverURL.Port() + "/repo.git"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "private network")

	svc.gitConf.AllowedPrivateHosts = []string{"gitea.internal", "127.0.0.1"}
	err = svc.Validate(context.Background(), &dto.GitSyncValidateRequest{RepoURL: serverURL.Scheme + "://gitea.internal:" + serverURL.Port() + "/repo.git"})
	require.NoError(t, err)
}

func TestGitRepoAllowedPrivateAddressValidation(t *testing.T) {
	validator := newGitRepoURLValidator([]string{"gitea.internal", "10.0.0.0/8"}, nil)
	err := validator.validateResolvedIPs("gitea.internal", []net.IP{net.ParseIP("192.168.1.20")})
	require.Error(t, err)
	require.True(t, errors.Is(err, errGitRepoPrivateNetwork))
}
