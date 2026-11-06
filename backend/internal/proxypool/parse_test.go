package proxypool

import (
	"testing"
	"time"
)

func TestParseResponseSupportsCanonicalAndProviderFormats(t *testing.T) {
	payloads := []string{
		`{"code":0,"message":"success","data":{"proxies":[{"protocol":"socks5","host":"10.0.0.1","port":1080,"expires_at":1769577600}]}}`,
		`{"code":"10001","msg":"获取成功","data":{"proxy_list":[{"ip":"203.25.208.163","port":1100,"protocol":"socks5","level":"高匿"}]}}`,
		`{"code":200,"message":"success","data":{"proxies":["192.168.1.1:8080","10.0.0.1:3128"],"count":2}}`,
		`["socks5://user:pass@example.test:1080"]`,
	}
	want := [][]Proxy{
		{{Protocol: "socks5", Host: "10.0.0.1", Port: 1080}},
		{{Protocol: "socks5", Host: "203.25.208.163", Port: 1100}},
		{{Protocol: "http", Host: "192.168.1.1", Port: 8080}, {Protocol: "http", Host: "10.0.0.1", Port: 3128}},
		{{Protocol: "socks5", Host: "example.test", Port: 1080, Username: "user", Password: "pass"}},
	}
	for index, payload := range payloads {
		proxies, err := ParseResponse([]byte(payload))
		if err != nil {
			t.Fatalf("payload %d: %v", index+1, err)
		}
		if len(proxies) != len(want[index]) {
			t.Fatalf("payload %d count=%d want=%d", index+1, len(proxies), len(want[index]))
		}
		for position, expected := range want[index] {
			actual := proxies[position]
			if actual.Protocol != expected.Protocol || actual.Host != expected.Host || actual.Port != expected.Port ||
				actual.Username != expected.Username || actual.Password != expected.Password {
				t.Fatalf("payload %d proxy=%+v want=%+v", index+1, actual, expected)
			}
		}
	}
}

func TestParseResponseRejectsInvalidPayloads(t *testing.T) {
	for _, payload := range []string{
		``,
		`{}`,
		`{"code":500,"message":"failed","data":[]}`,
		`{"code":0,"data":{"proxies":[]}}`,
		`{"code":0,"data":{"proxies":[{"host":"example.test"}]}}`,
		`{"code":0,"data":{"proxies":[{"protocol":"unknown","host":"example.test","port":1}]}}`,
	} {
		if _, err := ParseResponse([]byte(payload)); err == nil {
			t.Fatalf("expected error for %s", payload)
		}
	}
}

func TestProxyKeyAndURLHideCredentials(t *testing.T) {
	proxy := Proxy{Protocol: "socks5", Host: "example.test", Port: 1080, Username: "user", Password: "secret"}
	if got := proxy.Key(); got != "socks5://example.test:1080" {
		t.Fatalf("key=%q", got)
	}
	if got := proxy.URL().String(); got != "socks5://user:secret@example.test:1080" {
		t.Fatalf("url=%q", got)
	}
}

func TestParseExpiry(t *testing.T) {
	expiry, err := parseExpiry([]byte(`"2026-10-27T21:00:00Z"`))
	if err != nil || !expiry.Equal(time.Date(2026, 10, 27, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry=%v error=%v", expiry, err)
	}
	if _, err := parseExpiry([]byte(`"tomorrow"`)); err == nil {
		t.Fatal("expected invalid expiry")
	}
}
