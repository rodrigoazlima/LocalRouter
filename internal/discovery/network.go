package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// LocalRouterInstance represents a discovered LocalRouter instance on the network
type LocalRouterInstance struct {
	IP        string   `json:"ip"`
	Port      string   `json:"port"`
	Version   string   `json:"version"`
	Providers []string `json:"providers"`
	Models    []string `json:"models"`
}

// DiscoverLocalRouters attempts to discover other LocalRouter instances
// on the local network by scanning common ports.
func DiscoverLocalRouters(ctx context.Context, timeout time.Duration) ([]LocalRouterInstance, error) {
	instances := make([]LocalRouterInstance, 0)

	// Get local IP addresses to determine network range
	localIPs, err := getLocalIPs()
	if err != nil {
		return instances, fmt.Errorf("failed to get local IPs: %w", err)
	}

	// For each local IP, scan the local network
	for _, localIP := range localIPs {
		networkInstances, err := scanNetwork(ctx, localIP, timeout)
		if err == nil {
			instances = append(instances, networkInstances...)
		}
	}

	return instances, nil
}

// getLocalIPs returns all non-loopback IPv4 addresses
func getLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, i := range interfaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip != nil && !ip.IsLoopback() && ip.To4() != nil {
				ips = append(ips, ip)
			}
		}
	}

	return ips, nil
}

// scanNetwork scans the local network for LocalRouter instances on port 8080
func scanNetwork(ctx context.Context, localIP net.IP, timeout time.Duration) ([]LocalRouterInstance, error) {
	instances := make([]LocalRouterInstance, 0)

	// Extract network prefix (assume /24)
	ipStr := localIP.String()
	lastDot := strings.LastIndex(ipStr, ".")
	if lastDot == -1 {
		return instances, nil
	}
	networkPrefix := ipStr[:lastDot+1]

	// Scan IP range 1-254 on port 8080 (default LocalRouter port)
	for i := 1; i <= 254; i++ {
		ip := fmt.Sprintf("%s%d", networkPrefix, i)

		select {
		case <-ctx.Done():
			return instances, ctx.Err()
		default:
			if instance, err := probeInstance(ctx, ip, "8080", timeout/10); err == nil && instance != nil {
				instances = append(instances, *instance)
			}
		}
	}

	return instances, nil
}

// probeInstance probes a single IP:port for LocalRouter instance
func probeInstance(ctx context.Context, ip, port string, timeout time.Duration) (*LocalRouterInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s:%s/models", ip, port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode failed: %w", err)
	}

	if result.Object != "list" {
		return nil, fmt.Errorf("not a LocalRouter instance")
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}

	return &LocalRouterInstance{
		IP:     ip,
		Port:   port,
		Models: models,
	}, nil
}

// FormatAsYAML returns the instance info formatted as YAML snippet.
func (i LocalRouterInstance) FormatAsYAML() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("  - id: localrouter-%s", strings.ReplaceAll(i.IP, ".", "-")))
	lines = append(lines, "    type: openai-compatible")
	lines = append(lines, fmt.Sprintf("    endpoint: http://%s:%s/v1", i.IP, i.Port))
	lines = append(lines, "    recovery_window: 5m")
	lines = append(lines, "    models:")
	for _, model := range i.Models {
		lines = append(lines, fmt.Sprintf("      - id: %s", model))
	}
	return strings.Join(lines, "\n")
}
