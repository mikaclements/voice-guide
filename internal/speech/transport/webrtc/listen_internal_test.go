package webrtc

import (
	"net"
	"testing"
)

func TestAudioListensOnPrivateAddressesUnlessOthersAreNamed(t *testing.T) {
	tests := map[string]struct {
		named []net.IP
		ip    string
		keep  bool
	}{
		"a LAN address by default":                    {nil, "192.168.3.150", true},
		"loopback by default":                         {nil, "127.0.0.1", true},
		"a private IPv6 address by default":           {nil, "fd07:b51a:cc66::1", true},
		"not a public IPv6 address by default":        {nil, "2400:2410:30c0:2100::1", false},
		"not a public IPv4 address by default":        {nil, "203.0.113.7", false},
		"a named public address":                      {[]net.IP{net.ParseIP("203.0.113.7")}, "203.0.113.7", true},
		"not a private address when others are named": {[]net.IP{net.ParseIP("203.0.113.7")}, "192.168.3.150", false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := listenOn(tt.named)(net.ParseIP(tt.ip)); got != tt.keep {
				t.Errorf("listenOn(%v)(%s) = %v, want %v", tt.named, tt.ip, got, tt.keep)
			}
		})
	}
}

func TestAnOpusPacketsModeIsReadFromItsConfigurationNumber(t *testing.T) {
	// The configuration number is the first byte's top five bits; the rest
	// say stereo and how many frames, so they're set here to show they don't count.
	for config, want := range map[byte]string{0: "silk", 11: "silk", 12: "hybrid", 15: "hybrid", 16: "celt", 31: "celt"} {
		if got := mode(config<<3 | 0b111); got != want {
			t.Errorf("mode of configuration %d = %s, want %s", config, got, want)
		}
	}
}
