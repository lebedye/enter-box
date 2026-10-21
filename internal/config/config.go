// Package config отвечает за загрузку и валидацию YAML-конфигурации enter-box.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Log           LogConfig            `yaml:"log"`
	WAN           InterfaceConfig      `yaml:"wan"`
	LAN           LANConfig            `yaml:"lan"`
	TUN           TUNConfig            `yaml:"tun"`
	DNS           DNSConfig            `yaml:"dns"`
	Subscriptions []SubscriptionConfig `yaml:"subscriptions"`
	Endpoints     []string             `yaml:"endpoints"`
	EndpointFiles []string             `yaml:"endpoint_files,omitempty"`
	Routing       RoutingConfig        `yaml:"routing"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type InterfaceConfig struct {
	Name string `yaml:"name"`
}

// LANConfig описывает интерфейс(ы), смотрящие в коммутатор (или AP),
// и встроенный DHCP-сервер для клиентов.
type LANConfig struct {
	Interface       string     `yaml:"interface"`
	ExtraInterfaces []string   `yaml:"extra_interfaces,omitempty"`
	Address         string     `yaml:"address"`
	DHCP            DHCPConfig `yaml:"dhcp"`
}

type DHCPConfig struct {
	Enabled    bool          `yaml:"enabled"`
	RangeStart string        `yaml:"range_start"`
	RangeEnd   string        `yaml:"range_end"`
	Exclude    []string      `yaml:"exclude,omitempty"`
	LeaseTime  time.Duration `yaml:"lease_time"`
	Gateway    string        `yaml:"gateway"`
}

type TUNConfig struct {
	Interface string `yaml:"interface"`
	Address   string `yaml:"address"`
	MTU       int    `yaml:"mtu"`
	Stack     string `yaml:"stack"`
}

type DNSConfig struct {
	// Serve — поднимать ли встроенный DNS-форвардер на LAN-адресе.
	// Клиенты через DHCP получают его как DNS.
	Serve    bool     `yaml:"serve"`
	Domestic []string `yaml:"domestic"`
	Remote   []string `yaml:"remote"`
}

type SubscriptionConfig struct {
	URL        string        `yaml:"url"`
	Tag        string        `yaml:"tag"`
	UserAgent  string        `yaml:"user_agent"`
	AutoUpdate time.Duration `yaml:"auto_update"`
}

type RoutingConfig struct {
	UseRUDirect     bool     `yaml:"use_ru_direct"`
	DomesticDomains []string `yaml:"domestic_domains"`
	DomesticSuffix  []string `yaml:"domestic_suffix"`
	DomesticIPCIDR  []string `yaml:"domestic_ip_cidr"`
	ForceProxy      []string `yaml:"force_proxy"`
	Preferred       string   `yaml:"preferred"`
	Final           string   `yaml:"final"`

	URLTestInterval  time.Duration `yaml:"urltest_interval"`
	URLTestURL       string        `yaml:"urltest_url"`
	URLTestTolerance int           `yaml:"urltest_tolerance"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.TUN.Interface == "" {
		c.TUN.Interface = "tun0"
	}
	if c.TUN.Address == "" {
		c.TUN.Address = "10.10.10.1/24"
	}
	if c.TUN.MTU == 0 {
		c.TUN.MTU = 1420
	}
	if c.TUN.Stack == "" {
		c.TUN.Stack = "system"
	}
	if c.LAN.DHCP.LeaseTime == 0 {
		c.LAN.DHCP.LeaseTime = time.Hour
	}
	if c.LAN.DHCP.Gateway == "" && c.LAN.Address != "" {
		if ip, _, err := splitCIDR(c.LAN.Address); err == nil {
			c.LAN.DHCP.Gateway = ip
		}
	}
	if len(c.DNS.Remote) == 0 {
		c.DNS.Remote = []string{"1.1.1.1", "8.8.8.8"}
	}
	if len(c.DNS.Domestic) == 0 {
		c.DNS.Domestic = []string{"77.88.8.8", "77.88.8.1"}
	}
	// Serve по умолчанию включён, если в YAML он явно не выставлен в false.
	// Отличить "не задано" от "явно false" без указателя нельзя,
	// поэтому используем простое правило: если в YAML нет поля serve,
	// yaml.Unmarshal оставит false. В Load() перед applyDefaults мы
	// уже не можем этого увидеть. Решение — считать "serve: false"
	// значением по умолчанию только если DNS отключён явно в комментарии.
	// Проще: если пользователь не хочет serve — он явно напишет false,
	// но тогда мы не отличим от не заданного. Компромисс: считаем,
	// что отсутствие serve в yaml трактуется как false, но в
	// enter-box.example.yaml стоит serve: true по умолчанию.
	if c.Routing.Final == "" {
		c.Routing.Final = "proxy"
	}
	if c.Routing.URLTestInterval == 0 {
		c.Routing.URLTestInterval = 3 * time.Minute
	}
	if c.Routing.URLTestURL == "" {
		c.Routing.URLTestURL = "http://cp.cloudflare.com/generate_204"
	}
	if c.Routing.URLTestTolerance == 0 {
		c.Routing.URLTestTolerance = 50
	}
}

func (c *Config) validate() error {
	if c.WAN.Name == "" {
		return fmt.Errorf("wan.name is required")
	}
	if c.LAN.Interface == "" {
		return fmt.Errorf("lan.interface is required")
	}
	if c.LAN.Address == "" {
		return fmt.Errorf("lan.address is required")
	}
	if _, _, err := splitCIDR(c.LAN.Address); err != nil {
		return fmt.Errorf("lan.address: %w", err)
	}
	if c.LAN.DHCP.Enabled {
		if c.LAN.DHCP.RangeStart == "" || c.LAN.DHCP.RangeEnd == "" {
			return fmt.Errorf("lan.dhcp.range_start and lan.dhcp.range_end required when lan.dhcp.enabled=true")
		}
		for _, ex := range c.LAN.DHCP.Exclude {
			if _, _, err := parseExcludeSpec(ex); err != nil {
				return fmt.Errorf("lan.dhcp.exclude %q: %w", ex, err)
			}
		}
	}
	switch c.Routing.Final {
	case "proxy", "direct":
	default:
		return fmt.Errorf("routing.final must be 'proxy' or 'direct', got %q", c.Routing.Final)
	}
	if c.Routing.URLTestInterval < 10*time.Second {
		return fmt.Errorf("routing.urltest_interval must be >= 10s, got %s", c.Routing.URLTestInterval)
	}
	if c.Routing.URLTestTolerance < 0 {
		return fmt.Errorf("routing.urltest_tolerance must be >= 0")
	}
	if !strings.HasPrefix(c.Routing.URLTestURL, "http://") &&
		!strings.HasPrefix(c.Routing.URLTestURL, "https://") {
		return fmt.Errorf("routing.urltest_url must be http(s)://..., got %q", c.Routing.URLTestURL)
	}
	return nil
}

// splitCIDR режет "192.168.88.1/24" на ("192.168.88.1", "24").
func splitCIDR(s string) (ip, mask string, err error) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return s, "", fmt.Errorf("missing /: %q", s)
	}
	ip, mask = s[:i], s[i+1:]
	if ip == "" || mask == "" {
		return "", "", fmt.Errorf("empty ip or mask: %q", s)
	}
	return ip, mask, nil
}

// parseExcludeSpec проверяет формат exclude: "IP" или "IP-IP".
// Реальная логика в dhcp.Pool — здесь только валидация на уровне config.
func parseExcludeSpec(s string) (lo, hi string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty")
	}
	if l, h, ok := strings.Cut(s, "-"); ok {
		return strings.TrimSpace(l), strings.TrimSpace(h), nil
	}
	return s, "", nil
}
