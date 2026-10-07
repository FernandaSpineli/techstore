package catalog

import "testing"

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Fones Bluetooth Pró":     "fones-bluetooth-pro",
		"  iPhone 15 Pro Max  ":   "iphone-15-pro-max",
		"Cabo USB-C → Lightning":  "cabo-usb-c-lightning",
		"Câmeras & Acessórios!!!": "cameras-acessorios",
		"---":                     "",
	}
	for in, want := range tests {
		got := Slugify(in)
		if got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !slugPattern.MatchString(got) {
			t.Errorf("Slugify(%q) = %q does not match the DB slug constraint", in, got)
		}
	}
}

func TestPrefixQuery(t *testing.T) {
	tests := map[string]string{
		"iph":                     "iph:*",
		"iPhone 15":               "iphone:* & 15:*",
		"usb-c":                   "usb:* & c:*",
		"fone ' | ! & <-> :* (x)": "fone:* & x:*",
		"   ":                     "",
		"a b c d e f g h i j k":   "a:* & b:* & c:* & d:* & e:* & f:* & g:* & h:*",
		"câmera":                  "câmera:*",
	}
	for in, want := range tests {
		if got := prefixQuery(in); got != want {
			t.Errorf("prefixQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
