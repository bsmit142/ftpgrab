package config

// Status holds the status page configuration
type Status struct {
	Listen string     `yaml:"listen,omitempty" json:"listen,omitempty" validate:"required"`
	VPN    *StatusVPN `yaml:"vpn,omitempty" json:"vpn,omitempty" validate:"omitempty"`
}

// StatusVPN is the VPN the status page reports on
type StatusVPN struct {
	// Interface the server's traffic must leave through, e.g. wg0.
	Interface string `yaml:"interface,omitempty" json:"interface,omitempty" validate:"required"`
	// Probe is host:port of a DNS server only reachable through the VPN
	// (Mullvad: 10.64.0.1:53); an answer proves the tunnel carries traffic.
	Probe string `yaml:"probe,omitempty" json:"probe,omitempty"`
}
