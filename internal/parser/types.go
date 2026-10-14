// Package parser преобразует URI (vless://, vmess://, vpn://) в нормализованные
// Endpoint-структуры.
package parser

import "net/url"

type Kind string

const (
	KindVLESS   Kind = "vless"
	KindVMess   Kind = "vmess"
	KindAmnezia Kind = "amneziawg"
)

type Endpoint struct {
	Tag    string `json:"tag"`
	Kind   Kind   `json:"kind"`
	Server string `json:"server"`
	Port   uint16 `json:"port"`
	Raw    string `json:"raw"`

	UUID        string     `json:"uuid,omitempty"`
	Flow        string     `json:"flow,omitempty"`
	Security    string     `json:"security,omitempty"`
	SNI         string     `json:"sni,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	PublicKey   string     `json:"public_key,omitempty"`
	ShortID     string     `json:"short_id,omitempty"`
	Network     string     `json:"network,omitempty"`
	WSPath      string     `json:"ws_path,omitempty"`
	WSHost      string     `json:"ws_host,omitempty"`
	GRPCService string     `json:"grpc_service,omitempty"`
	AlterID     int        `json:"alter_id,omitempty"`
	Cipher      string     `json:"cipher,omitempty"`
	Params      url.Values `json:"params,omitempty"`

	AWG *AmneziaWGParams `json:"awg,omitempty"`
}

type AmneziaWGParams struct {
	PrivateKey string   `json:"private_key"`
	PeerPublic string   `json:"peer_public"`
	Preshared  string   `json:"preshared,omitempty"`
	Address    []string `json:"address"`
	DNS        []string `json:"dns,omitempty"`
	MTU        int      `json:"mtu,omitempty"`
	Jc         int      `json:"jc,omitempty"`
	Jmin       int      `json:"jmin,omitempty"`
	Jmax       int      `json:"jmax,omitempty"`
	S1         int      `json:"s1,omitempty"`
	S2         int      `json:"s2,omitempty"`
	H1         int      `json:"h1,omitempty"`
	H2         int      `json:"h2,omitempty"`
	H3         int      `json:"h3,omitempty"`
	H4         int      `json:"h4,omitempty"`
	I1         string   `json:"i1,omitempty"`
	I2         string   `json:"i2,omitempty"`
	I3         string   `json:"i3,omitempty"`
	I4         string   `json:"i4,omitempty"`
	I5         string   `json:"i5,omitempty"`
}

// AwgIfaceName — единый источник истины для имени AWG-интерфейса.
// Обе стороны (netcfg при создании, singbox при bind) зовут эту функцию.
func AwgIfaceName(ep *Endpoint) string {
	return "awg-" + sanitizeTag(ep.Tag)
}

func sanitizeTag(t string) string {
	b := make([]byte, 0, len(t))
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b = append(b, c)
		case c == '-' || c == '_':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
		if len(b) >= 10 {
			break
		}
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}
