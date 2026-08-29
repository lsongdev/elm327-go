package elm327

import (
	"reflect"
	"testing"
)

func TestExtractMode01Responses(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want [][]byte
	}{
		{"normal with echo", "010C\n41 0C 1A F8", [][]byte{{0x1A, 0xF8}}},
		{"spaces disabled", "410C1AF8", [][]byte{{0x1A, 0xF8}}},
		{"11 bit header", "7E8 04 41 0C 1A F8", [][]byte{{0x1A, 0xF8}}},
		{"compact 11 bit header", "7E804410C1AF8", [][]byte{{0x1A, 0xF8}}},
		{"29 bit header", "18DAF110 04 41 0C 1A F8", [][]byte{{0x1A, 0xF8}}},
		{"search and multiple ECUs", "SEARCHING...\n7E8 04 41 0C 1A F8\n7E9 04 41 0C 20 00", [][]byte{{0x1A, 0xF8}, {0x20, 0x00}}},
		{"CAF0 without header", "04410C1AF8", [][]byte{{0x1A, 0xF8}}},
		{"numbered multiline", "006\n0: 41 0C 1A F8\n1: AA BB", [][]byte{{0x1A, 0xF8, 0xAA, 0xBB}}},
		{"positive data containing 7F01", "41 0C 7F 01", [][]byte{{0x7F, 0x01}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractMode01Responses("010C", tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got % X, want % X", got, tt.want)
			}
		})
	}
}

func TestExtractMode01Errors(t *testing.T) {
	for _, response := range []string{"NO DATA", "CAN ERROR", "UNABLE TO CONNECT", "7E8 03 7F 01 11"} {
		t.Run(response, func(t *testing.T) {
			if _, err := extractMode01Responses("010C", response); err == nil {
				t.Fatalf("expected error for %q", response)
			}
		})
	}
	if _, err := extractMode01Responses("01ZZ", "41 0C 00 00"); err == nil {
		t.Fatal("expected invalid PID error")
	}
}

func FuzzExtractMode01Responses(f *testing.F) {
	for _, seed := range []string{"41 0C 1A F8", "7E804410C1AF8", "NO DATA", "SEARCHING...\r41 0C 00 00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, response string) {
		payloads, err := extractMode01Responses("010C", response)
		if err == nil && len(payloads) == 0 {
			t.Fatal("successful parse returned no payloads")
		}
		if err == nil {
			for _, payload := range payloads {
				if len(payload) == 0 {
					t.Fatal("successful parse yielded an empty payload")
				}
			}
		}
	})
}
