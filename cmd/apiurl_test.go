package cmd

import (
	"strings"
	"testing"
)

func TestNewBoxSSHCarriesAPIURL(t *testing.T) {
	t.Setenv("PATH", t.TempDir()+":"+"/usr/bin:/bin")
	old := apiURLFlag
	defer func() { apiURLFlag = old }()
	proxy := func() string {
		b, err := newBoxSSH("box1", "/tmp/k/id")
		if err != nil {
			t.Skip(err)
		}
		for i, a := range b.args {
			if i > 0 && b.args[i-1] == "-o" && strings.HasPrefix(a, "ProxyCommand=") {
				return a
			}
		}
		t.Fatal("no ProxyCommand")
		return ""
	}
	apiURLFlag = ""
	if p := proxy(); strings.Contains(p, "--api-url") {
		t.Errorf("no flag, yet %q", p)
	}
	apiURLFlag = "https://vms.example.test"
	if p := proxy(); !strings.Contains(p, "--api-url 'https://vms.example.test' ssh-proxy") {
		t.Errorf("flag missing: %q", p)
	}
}

func TestDashboardBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(dashboardEnv, "")
	if got := dashboardBase(); got != "https://boxctl.io" {
		t.Errorf("default = %q", got)
	}
	t.Setenv(dashboardEnv, "https://dash.example.test/")
	if got := dashboardBase(); got != "https://dash.example.test" {
		t.Errorf("env = %q", got)
	}
}
