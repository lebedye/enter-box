// Package amneziaapi получает рабочий AmneziaWG-конфиг из
// короткого Premium-ключа vpn://. Запрос шифруется точно так же,
// как в официальном клиенте AmneziaVPN.
package amneziaapi

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/amnezia-vpn/libagw/gateway"
)

const (
	defaultGateway = "http://gw.amnezia.org:80/v1/config"
	clientVersion  = "5.0.3.0"
	clientName     = "AmneziaVPN"
)

var (
	// Официальный клиент использует эти публичные хранилища для получения
	// списка обходных API-узлов, когда оператор блокирует основной gateway.
	primaryProxyStorages = []string{
		"https://s3.eu-north-1.amazonaws.com/amnezia/",
		"https://storage.googleapis.com/lambda-list/",
		"https://amnzstrg01.blob.core.windows.net/lambda-list/",
		"https://objectstorage.eu-zurich-1.oraclecloud.com/n/zrhfyaq6qxvh/b/lambda-list/o/",
	}
	fallbackProxyStorages = []string{
		"https://storage.mwsapis.ru/lambda-list/",
		"https://46.8.209.252/lambda-list/",
		"https://s3.cloudruserapi.net/lambda-list/",
	}
)

// productionPublicKey — открытый RSA-ключ официального
// production-шлюза. Это не секрет: он нужен только для шифрования
// запроса, расшифровать его может только Amnezia.
const productionPublicKey = `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAj5mxl/4DL3Sk89ntxs5G
X3JawGQWIoq6rvNkOzNGuNgedNS2+pi6hZl3Izl1Io9om4KiUlMT6mgLO1hTr9q+
s7CYhlvroFA7ErucF+9L+7FCt0Igi0kIK/R2/vxd/2HaUrorn/aSvvutkYwbfxqW
SwtzE+RuBeDWGvEt937OW0oqYONPYv9E4T56Dz/EZ6v2t8ejAnKLbGD/GocMmipK
7etFSiSMAB2RmaztqTq4NleBepfO80XpYlW9pCSXuHcE8wxHczkzxsbyMAMsG/K3
vUQY6qPtohqqzSSBwa/8u2ptNHBeor7l7DdYXeR/Nqcc4z92VUkZ5lOVR4evkS5V
/wQqp5tnOJEj3NjUhEhXFoNEapbZd1bh6iQoUk7jC1TdvKJ/nPKGZAsHRpr0rNKz
fx/N/Oo6lr2yh/+ps6VxTkbPmB6E85WOO3UvjImZUY0XQdBjWle/4iJLdEC77Nr0
jXhdgeypucy6jkB6iBHMeVMlrNMEV7UxoBR/cCNx55zu/8sml5ByiDvCDT7sRomN
NgVt5S/FaVjYuzFUifJ12ToChXFgESKFmuso7WluEaWvMIGREdrMrKQKHfYLOzWF
2B5ZJDqw4o03fU4J/6rw61M1b+rjVpXMjPnzc2A+RgcjTvXv955gfZkwe4lt5wk/
3j8zMVo3+zLrMTAaEeIUM0UCAwEAAQ==
-----END PUBLIC KEY-----`

// Bootstrap — несекретные параметры и закрытые данные
// авторизации из Premium-файла. AuthData нигде не логируется.
type Bootstrap struct {
	ConfigVersion int             `json:"config_version"`
	APIConfig     APIConfig       `json:"api_config"`
	AuthData      json.RawMessage `json:"auth_data"`
}

type APIConfig struct {
	ServiceType     string `json:"service_type"`
	ServiceProtocol string `json:"service_protocol"`
	UserCountryCode string `json:"user_country_code"`
}

type gatewayEnvelope struct {
	KeyPayload string `json:"key_payload"`
	APIPayload string `json:"api_payload"`
}

// IsBootstrapVPN отличает Premium-ключ от уже готового
// экспорта AmneziaWG. Это позволяет не отправлять в сеть
// обычные vpn:// конфиги.
func IsBootstrapVPN(raw string) bool {
	var b Bootstrap
	if err := decodeVPNJSON(raw, &b); err != nil {
		return false
	}
	return b.ConfigVersion == 2 && len(b.AuthData) > 0 && b.APIConfig.ServiceType != ""
}

// Resolve обменивает Premium-ключ на готовый vpn:// с
// подставленным приватным ключом клиента.
func Resolve(ctx context.Context, raw string) (string, error) {
	var bootstrap Bootstrap
	if err := decodeVPNJSON(raw, &bootstrap); err != nil {
		return "", fmt.Errorf("decode premium vpn key: %w", err)
	}
	if bootstrap.ConfigVersion != 2 || bootstrap.APIConfig.ServiceType == "" || len(bootstrap.AuthData) == 0 {
		return "", errors.New("vpn key is not an Amnezia Premium bootstrap config")
	}

	privateKey, publicKey, err := wireGuardKeyPair()
	if err != nil {
		return "", fmt.Errorf("generate client key: %w", err)
	}
	payload, err := buildAPIPayload(bootstrap, publicKey)
	if err != nil {
		return "", err
	}
	response, err := requestConfig(ctx, bootstrap, payload)
	if err != nil {
		return "", err
	}

	var apiResponse struct {
		Config     string `json:"config"`
		HTTPStatus int    `json:"http_status"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(response, &apiResponse); err != nil {
		return "", fmt.Errorf("decode gateway response: %w", err)
	}
	if apiResponse.HTTPStatus >= 400 {
		return "", fmt.Errorf("Amnezia gateway error %d: %s", apiResponse.HTTPStatus, apiResponse.Message)
	}
	if !strings.HasPrefix(apiResponse.Config, "vpn://") {
		return "", errors.New("Amnezia gateway returned an empty config")
	}

	// Сервер возвращает шаблон. Приватный ключ остаётся
	// только на этом устройстве и не передаётся в Amnezia.
	resolved, err := replaceVPNPlaceholder(apiResponse.Config, privateKey)
	if err != nil {
		return "", fmt.Errorf("prepare returned config: %w", err)
	}
	return resolved, nil
}

func requestConfig(ctx context.Context, bootstrap Bootstrap, payload []byte) ([]byte, error) {
	if endpoint := gatewayEndpoint(); endpoint != defaultGateway {
		// Переопределённый адрес применяется для локального SSH-туннеля
		// во время диагностики. Обычная установка использует failover ниже.
		return postEncrypted(ctx, endpoint, payload)
	}
	client := gateway.New(gateway.Config{
		GatewayEndpoint:     "http://gw.amnezia.org:80/",
		PublicKeyPEM:        []byte(productionPublicKey),
		S3PrimaryEndpoints:  primaryProxyStorages,
		S3FallbackEndpoints: fallbackProxyStorages,
		RequestTimeout:      15 * time.Second,
		ProxyRequestTimeout: 15 * time.Second,
	})
	result, err := client.Post(ctx, "v1/config", payload, gateway.PostOptions{
		ServiceType:     bootstrap.APIConfig.ServiceType,
		UserCountryCode: bootstrap.APIConfig.UserCountryCode,
	})
	if err != nil {
		return nil, fmt.Errorf("request Amnezia gateway with failover: %w", err)
	}
	return result.Body, nil
}

func buildAPIPayload(b Bootstrap, publicKey string) ([]byte, error) {
	var authData any
	if err := json.Unmarshal(b.AuthData, &authData); err != nil {
		return nil, fmt.Errorf("decode auth_data: %w", err)
	}
	p := map[string]any{
		"os_version":       runtime.GOOS,
		"app_version":      clientVersion,
		"cli_name":         clientName,
		"app_language":     "ru",
		"installation_uuid": stableInstallationUUID(b.AuthData),
		"user_country_code": b.APIConfig.UserCountryCode,
		"service_type":      b.APIConfig.ServiceType,
		"service_protocol":  b.APIConfig.ServiceProtocol,
		"public_key":        publicKey,
		"auth_data":         authData,
		"is_connect_event":  true,
	}
	return json.Marshal(p)
}

func stableInstallationUUID(authData []byte) string {
	// machine-id различает платы, а auth_data различает подписки.
	// Так перезапуск не занимает новый слот устройства.
	machineID, _ := os.ReadFile("/etc/machine-id")
	sum := sha256.Sum256(append(bytes.TrimSpace(machineID), authData...))
	id := append([]byte(nil), sum[:16]...)
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

func wireGuardKeyPair() (privateB64, publicB64 string, err error) {
	private := make([]byte, 32)
	if _, err := rand.Read(private); err != nil {
		return "", "", err
	}
	// WireGuard использует X25519 с обязательным clamp скаляра.
	private[0] &= 248
	private[31] &= 127
	private[31] |= 64
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(private),
		base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func gatewayEndpoint() string {
	// Переопределение нужно только для диагностики или первичной настройки
	// через локальный SSH-туннель, если оператор блокирует адрес шлюза.
	if endpoint := strings.TrimSpace(os.Getenv("AMNEZIA_GATEWAY_URL")); endpoint != "" {
		return endpoint
	}
	return defaultGateway
}

func postEncrypted(ctx context.Context, endpoint string, payload []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(productionPublicKey))
	if block == nil {
		return nil, errors.New("invalid embedded gateway public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse gateway public key: %w", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("gateway key is not RSA")
	}

	key := make([]byte, 32)
	iv := make([]byte, 32)
	salt := make([]byte, 8)
	for _, dst := range [][]byte{key, iv, salt} {
		if _, err := rand.Read(dst); err != nil {
			return nil, err
		}
	}
	keyJSON, err := json.Marshal(map[string]string{
		"aes_key":  base64.StdEncoding.EncodeToString(key),
		"aes_iv":   base64.StdEncoding.EncodeToString(iv),
		"aes_salt": base64.StdEncoding.EncodeToString(salt),
	})
	if err != nil {
		return nil, err
	}
	encryptedKey, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, keyJSON)
	if err != nil {
		return nil, fmt.Errorf("encrypt gateway key: %w", err)
	}
	encryptedPayload, err := encryptCBC(payload, key, iv[:aes.BlockSize])
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(gatewayEnvelope{
		KeyPayload: base64.StdEncoding.EncodeToString(encryptedKey),
		APIPayload: base64.StdEncoding.EncodeToString(encryptedPayload),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-ID", randomRequestID())
	// При первичной настройке через SSH-туннель URL указывает на localhost,
	// но виртуальному хосту шлюза всё равно требуется штатное имя Host.
	if endpoint != defaultGateway {
		req.Host = "gw.amnezia.org"
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Amnezia gateway: %w", err)
	}
	defer resp.Body.Close()
	ciphertext, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read Amnezia gateway: %w", err)
	}
	plain, err := decryptCBC(ciphertext, key, iv[:aes.BlockSize])
	if err != nil {
		return nil, fmt.Errorf("decrypt Amnezia gateway response (HTTP %d): %w", resp.StatusCode, err)
	}
	return plain, nil
}

func encryptCBC(plain, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func decryptCBC(ciphertext, key, iv []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("invalid encrypted response length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, errors.New("invalid encrypted response padding")
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid encrypted response padding bytes")
		}
	}
	return out[:len(out)-pad], nil
}

func randomRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func decodeVPNJSON(raw string, out any) error {
	payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "vpn://"))
	// URL-safe base64 из Qt может быть как с padding "=", так и без
	// него. RawURLEncoding работает с обоими вариантами после обрезки.
	payload = strings.TrimRight(payload, "=")
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Часть клиентов экспортирует standard base64 с padding.
		decoded, err = base64.StdEncoding.DecodeString(payload)
	}
	if err != nil {
		return fmt.Errorf("base64: %w", err)
	}
	plain, err := unpackQtCompressed(decoded)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	return nil
}

func unpackQtCompressed(data []byte) ([]byte, error) {
	// qCompress добавляет перед zlib четырёхбайтную длину.
	if len(data) >= 6 {
		zr, err := zlib.NewReader(bytes.NewReader(data[4:]))
		if err == nil {
			plain, readErr := io.ReadAll(zr)
			_ = zr.Close()
			// Некоторые версии Amnezia записывают неточный размер в заголовке.
			// Валидный распакованный JSON является более надёжной проверкой.
			if readErr == nil && json.Valid(plain) {
				return plain, nil
			}
		}
	}
	// Старые экспорты могут содержать обычный JSON.
	if json.Valid(data) {
		return data, nil
	}
	return nil, errors.New("unsupported vpn:// payload (not qCompress or JSON)")
}

func replaceVPNPlaceholder(raw, privateKey string) (string, error) {
	payload := strings.TrimPrefix(strings.TrimSpace(raw), "vpn://")
	payload = strings.TrimRight(payload, "=")
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	plain, err := unpackQtCompressed(decoded)
	if err != nil {
		return "", err
	}
	plain = bytes.ReplaceAll(plain, []byte("$WIREGUARD_CLIENT_PRIVATE_KEY"), []byte(privateKey))
	if bytes.Contains(plain, []byte("$WIREGUARD_CLIENT_PRIVATE_KEY")) {
		return "", errors.New("private key placeholder was not replaced")
	}

	var compressed bytes.Buffer
	// qCompress хранит длину исходного JSON в big-endian перед zlib.
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(plain)))
	compressed.Write(size[:])
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(plain); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return "vpn://" + base64.RawURLEncoding.EncodeToString(compressed.Bytes()), nil
}
