package mitm

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
)

// InterceptedDomains maps tool names to their domains for DNS interception.
var InterceptedDomains = map[string][]string{
	"antigravity": {
		"cloudcode-pa.googleapis.com",
		"daily-cloudcode-pa.googleapis.com",
	},
	"codex": {
		"chatgpt.com",
		"api.chatgpt.com",
	},
	"kiro": {
		"runtime.us-east-1.kiro.dev",
	},
	"copilot": {
		"api.githubcopilot.com",
	},
	"cursor": {
		"api.cursor.com",
		"api2.cursor.com",
	},
}

// AllDomains returns all intercepted domains across all tools.
func AllDomains() []string {
	var out []string
	for _, domains := range InterceptedDomains {
		out = append(out, domains...)
	}
	return out
}

// AddHostsEntries adds /etc/hosts entries redirecting intercepted domains to localhost.
func AddHostsEntries() error {
	entries := hostsEntries()
	cmd := fmt.Sprintf("echo '%s' | sudo tee -a /etc/hosts > /dev/null", entries)
	return runSudo(cmd)
}

// RemoveHostsEntries removes /etc/hosts entries added by 9router.
func RemoveHostsEntries() error {
	if runtime.GOOS == "darwin" {
		cmd := `sudo sed -i '' '/# 9router-mitm/d' /etc/hosts`
		return runSudo(cmd)
	}
	cmd := `sudo sed -i '/# 9router-mitm/d' /etc/hosts`
	return runSudo(cmd)
}

// CheckDNSStatus verifies that all domains resolve to 127.0.0.1.
func CheckDNSStatus() (map[string]bool, error) {
	results := make(map[string]bool)
	for _, domain := range AllDomains() {
		addrs, err := net.LookupHost(domain)
		results[domain] = err == nil && len(addrs) > 0 && addrs[0] == "127.0.0.1"
	}
	return results, nil
}

func hostsEntries() string {
	var b strings.Builder
	b.WriteString("\n# 9router-mitm\n")
	for _, d := range AllDomains() {
		b.WriteString(fmt.Sprintf("127.0.0.1 %s\n", d))
	}
	b.WriteString("# end 9router-mitm\n")
	return b.String()
}

func runSudo(cmd string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("Windows MITM not yet supported")
	}
	c := exec.Command("sh", "-c", cmd)
	return c.Run()
}
