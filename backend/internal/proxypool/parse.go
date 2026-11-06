package proxypool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type rawEnvelope struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
	Proxies json.RawMessage `json:"proxies"`
}

// ParseResponse accepts the canonical aggregator response documented in
// docs/PROXY_POOL_API.md. It also accepts the two provider formats commonly
// used for free proxy APIs: data.proxy_list objects and data.proxies strings.
func ParseResponse(body []byte) ([]Proxy, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errors.New("proxy pool returned an empty response")
	}
	if body[0] == '[' {
		return parseProxyArray(body)
	}
	var envelope rawEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode proxy pool response: %w", err)
	}
	if len(envelope.Code) > 0 {
		code := strings.Trim(string(envelope.Code), `"`)
		switch code {
		case "0", "200", "10001":
		default:
			message := strings.TrimSpace(envelope.Message)
			if message == "" {
				message = strings.TrimSpace(envelope.Msg)
			}
			return nil, fmt.Errorf("proxy pool reported code %s: %s", code, message)
		}
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return parseProxyArray(envelope.Proxies)
	}
	if data[0] == '[' {
		return parseProxyArray(data)
	}
	var container struct {
		Proxies   json.RawMessage `json:"proxies"`
		ProxyList json.RawMessage `json:"proxy_list"`
	}
	if err := json.Unmarshal(data, &container); err != nil {
		return nil, fmt.Errorf("decode proxy pool data: %w", err)
	}
	if len(bytes.TrimSpace(container.Proxies)) > 0 {
		proxies, err := parseProxyArray(container.Proxies)
		if err != nil || len(proxies) > 0 {
			return proxies, err
		}
	}
	if len(bytes.TrimSpace(container.ProxyList)) > 0 {
		return parseProxyArray(container.ProxyList)
	}
	// Also accept a map keyed by proxy address. This keeps metadata-rich
	// aggregator responses possible while preferring the ordered array format.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(data, &keyed); err == nil && len(keyed) > 0 {
		items := make([]json.RawMessage, 0, len(keyed))
		keys := make([]string, 0, len(keyed))
		for key := range keyed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := bytes.TrimSpace(keyed[key])
			if len(value) == 0 || bytes.Equal(value, []byte("null")) {
				continue
			}
			if value[0] == '{' {
				var object map[string]json.RawMessage
				if json.Unmarshal(value, &object) != nil {
					continue
				}
				if _, exists := object["host"]; !exists {
					if _, exists := object["ip"]; !exists {
						encoded, _ := json.Marshal(key)
						object["host"] = encoded
					}
				}
				value, _ = json.Marshal(object)
			} else {
				encoded, _ := json.Marshal(key)
				value = encoded
			}
			items = append(items, value)
		}
		if len(items) > 0 {
			encoded, _ := json.Marshal(items)
			return parseProxyArray(encoded)
		}
	}
	return nil, errors.New("proxy pool response has no proxy list")
}

func parseProxyArray(raw json.RawMessage) ([]Proxy, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, errors.New("proxy pool response has no proxy list")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode proxy list: %w", err)
	}
	result := make([]Proxy, 0, len(items))
	var invalid []string
	for index, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 {
			invalid = append(invalid, fmt.Sprintf("item %d is empty", index+1))
			continue
		}
		var proxy Proxy
		var err error
		if item[0] == '"' {
			var value string
			if err = json.Unmarshal(item, &value); err == nil {
				proxy, err = parseProxyString(value)
			}
		} else if item[0] == '{' {
			proxy, err = parseProxyObject(item)
		} else {
			err = errors.New("must be a string or object")
		}
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("item %d: %v", index+1, err))
			continue
		}
		result = append(result, proxy)
	}
	if len(result) == 0 {
		detail := "proxy list is empty"
		if len(invalid) > 0 {
			detail = strings.Join(invalid[:min(len(invalid), 3)], "; ")
		}
		return nil, fmt.Errorf("no valid proxies: %s", detail)
	}
	return result, nil
}

func parseProxyObject(raw json.RawMessage) (Proxy, error) {
	var object struct {
		IP        string          `json:"ip"`
		Host      string          `json:"host"`
		Port      json.RawMessage `json:"port"`
		Protocol  string          `json:"protocol"`
		Scheme    string          `json:"scheme"`
		Username  string          `json:"username"`
		Password  string          `json:"password"`
		ExpiresAt json.RawMessage `json:"expires_at"`
		ExpireAt  json.RawMessage `json:"expire_at"`
		Enabled   *bool           `json:"enabled"`
		Status    string          `json:"status"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return Proxy{}, fmt.Errorf("decode object: %w", err)
	}
	if object.Enabled != nil && !*object.Enabled {
		return Proxy{}, errors.New("proxy is disabled")
	}
	switch strings.ToLower(strings.TrimSpace(object.Status)) {
	case "", "ok", "active", "available", "healthy":
	default:
		return Proxy{}, fmt.Errorf("proxy status %q is not usable", object.Status)
	}
	host := strings.TrimSpace(object.Host)
	if host == "" {
		host = strings.TrimSpace(object.IP)
	}
	port, err := parsePort(object.Port)
	if err != nil {
		return Proxy{}, err
	}
	protocol := strings.TrimSpace(object.Protocol)
	if protocol == "" {
		protocol = strings.TrimSpace(object.Scheme)
	}
	if protocol == "" {
		protocol = "http"
	}
	proxy, err := validateProxy(protocol, host, port, object.Username, object.Password)
	if err != nil {
		return Proxy{}, err
	}
	rawExpiry := object.ExpiresAt
	if len(bytes.TrimSpace(rawExpiry)) == 0 {
		rawExpiry = object.ExpireAt
	}
	if len(bytes.TrimSpace(rawExpiry)) > 0 {
		expiry, err := parseExpiry(rawExpiry)
		if err != nil {
			return Proxy{}, err
		}
		proxy.ExpiresAt = expiry
	}
	return proxy, nil
}

func parseProxyString(value string) (Proxy, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Proxy{}, errors.New("empty proxy string")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return Proxy{}, fmt.Errorf("invalid proxy URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Proxy{}, errors.New("proxy URL must not contain a path")
	}
	portText := parsed.Port()
	if portText == "" {
		switch parsed.Scheme {
		case "http":
			portText = "80"
		case "https":
			portText = "443"
		default:
			portText = "1080"
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Proxy{}, errors.New("proxy port must be numeric")
	}
	username, password := "", ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	return validateProxy(parsed.Scheme, parsed.Hostname(), port, username, password)
}

func validateProxy(protocol, host string, port int, username, password string) (Proxy, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "socks" {
		protocol = "socks5"
	}
	switch protocol {
	case "http", "https", "socks5", "socks5h":
	default:
		return Proxy{}, fmt.Errorf("unsupported protocol %q", protocol)
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return Proxy{}, errors.New("proxy host is required")
	}
	if net.ParseIP(host) == nil {
		if len(host) > 253 || strings.ContainsAny(host, " /\\:@?#") {
			return Proxy{}, fmt.Errorf("invalid proxy host %q", host)
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return Proxy{}, fmt.Errorf("invalid proxy host %q", host)
			}
			for _, char := range label {
				if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
					(char < '0' || char > '9') && char != '-' {
					return Proxy{}, fmt.Errorf("invalid proxy host %q", host)
				}
			}
		}
	}
	if port < 1 || port > 65535 {
		return Proxy{}, fmt.Errorf("proxy port %d is out of range", port)
	}
	if strings.ContainsAny(username, "\r\n") || strings.ContainsAny(password, "\r\n") {
		return Proxy{}, errors.New("proxy credentials must not contain line breaks")
	}
	return Proxy{Protocol: protocol, Host: host, Port: port, Username: username, Password: password}, nil
}

func parsePort(raw json.RawMessage) (int, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || value == "null" {
		return 0, errors.New("proxy port is required")
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New("proxy port must be numeric")
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("proxy port %d is out of range", port)
	}
	return port, nil
}

func parseExpiry(raw json.RawMessage) (time.Time, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || value == "null" || value == "0" {
		return time.Time{}, nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds > 1_000_000_000_000 {
			seconds /= 1000
		}
		return time.Unix(seconds, 0), nil
	}
	expiry, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("expires_at must be RFC3339 or a Unix timestamp")
	}
	return expiry, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}
