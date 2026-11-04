package elm327

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
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

func TestParseMessages(t *testing.T) {
	const vin = "1G1JC5444R7252367"
	vinHex := fmt.Sprintf("%X", vin)
	tests := []struct {
		name    string
		request string
		raw     string
		want    []Message
	}{
		{"real adapter capture", "0100", "SEARCHING...\r7E8064100BE3FA813\r", []Message{{"7E8", []byte{0x41, 0x00, 0xBE, 0x3F, 0xA8, 0x13}}}},
		{"can padding is removed", "010D", "7E8 03 41 0D 3C AA AA AA AA", []Message{{"7E8", []byte{0x41, 0x0D, 0x3C}}}},
		{"echo is removed", "010D", "010D\r7E8 03 41 0D 3C", []Message{{"7E8", []byte{0x41, 0x0D, 0x3C}}}},
		{"sorted by ecu", "010D", "7E9 03 41 0D 01\r7E8 03 41 0D 02", []Message{{"7E8", []byte{0x41, 0x0D, 0x02}}, {"7E9", []byte{0x41, 0x0D, 0x01}}}},
		{"29 bit", "010D", "18DAF110 03 41 0D 3C", []Message{{"18DAF110", []byte{0x41, 0x0D, 0x3C}}}},
		{"29 bit with spaces between bytes", "010D", "18 DA F1 10 03 41 0D 3C", []Message{{"18DAF110", []byte{0x41, 0x0D, 0x3C}}}},
		{"iso 9141 with header and checksum", "010D", "48 6B 10 41 0D 3C 99", []Message{{"10", []byte{0x41, 0x0D, 0x3C}}}},
		{"kwp with header and checksum", "010D", "83 F1 10 41 0D 3C 99", []Message{{"10", []byte{0x41, 0x0D, 0x3C}}}},
		{"response pending is skipped", "0902", "7E8 03 7F 09 78\r7E8 04 49 02 01 31", []Message{{"7E8", []byte{0x49, 0x02, 0x01, 0x31}}}},
		{"data with 7F after service is positive", "010C", "7E8 04 41 0C 7F 01", []Message{{"7E8", []byte{0x41, 0x0C, 0x7F, 0x01}}}},
		{"caf0 data resembling a service", "010C", "04 41 0C 41 0D", []Message{{"", []byte{0x41, 0x0C, 0x41, 0x0D}}}},
		{"clone without pci", "010D", "7E8 41 0D 3C", []Message{{"7E8", []byte{0x41, 0x0D, 0x3C}}}},
		{"nodata without space beside data", "010D", "NODATA\r7E8 03 41 0D 3C", []Message{{"7E8", []byte{0x41, 0x0D, 0x3C}}}},
		{"corrupt frame beside good one", "010D", "7E9 03 41 0D <RX ERROR\r7E8 03 41 0D 3C", []Message{{"7E8", []byte{0x41, 0x0D, 0x3C}}}},
		{
			"isotp vin with headers, two ecus interleaved", "0902",
			"7E8 10 14 49 02 01 " + spaced(vinHex[:6]) + "\r" +
				"7E9 03 49 02 AA\r" +
				"7E8 21 " + spaced(vinHex[6:20]) + "\r" +
				"7E8 22 " + spaced(vinHex[20:]) + " AA AA",
			[]Message{
				{"7E8", append([]byte{0x49, 0x02, 0x01}, vin...)},
				{"7E9", []byte{0x49, 0x02, 0xAA}},
			},
		},
		{
			"numbered multi-frame without headers", "0902",
			"014\r0: 49 02 01 " + spaced(vinHex[:6]) + "\r1: " + spaced(vinHex[6:20]) + "\r2: " + spaced(vinHex[20:]) + " 00 00",
			[]Message{{"", append([]byte{0x49, 0x02, 0x01}, vin...)}},
		},
		{
			"legacy vin frames merge", "0902",
			"49 02 01 00 00 00 31\r49 02 02 47 31 4A 43\r49 02 03 35 34 34 34\r49 02 04 52 37 32 35\r49 02 05 32 33 36 37",
			[]Message{{"", append([]byte{0x49, 0x02, 0x01, 0, 0, 0}, vin...)}},
		},
		{
			"legacy dtc frames merge per ecu", "03",
			"48 6B 10 43 03 01 03 02 03 03 99\r48 6B 10 43 03 04 00 00 00 00 98",
			[]Message{{"10", []byte{0x43, 0x03, 0x01, 0x03, 0x02, 0x03, 0x03, 0x03, 0x04, 0, 0, 0, 0}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMessages(tt.request, tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func spaced(hexDigits string) string {
	var parts []string
	for i := 0; i+1 < len(hexDigits); i += 2 {
		parts = append(parts, hexDigits[i:i+2])
	}
	return strings.Join(parts, " ")
}

func TestParseMessagesErrors(t *testing.T) {
	var adapter *AdapterError
	var refused *NegativeResponseError
	if _, err := ParseMessages("010C", "NO DATA"); !errors.Is(err, ErrNoData) {
		t.Errorf("NO DATA = %v", err)
	}
	if _, err := ParseMessages("010C", "NODATA"); !errors.Is(err, ErrNoData) {
		t.Errorf("NODATA = %v", err)
	}
	for _, status := range []string{"UNABLE TO CONNECT", "CAN ERROR", "BUS INIT: ...ERROR", "ERR94", "?", "BUFFER FULL", "7E8 03 41 <CRC ERROR"} {
		if _, err := ParseMessages("010C", "SEARCHING...\r"+status); !errors.As(err, &adapter) {
			t.Errorf("%q = %v", status, err)
		}
	}
	_, err := ParseMessages("04", "7E8 03 7F 04 22")
	if !errors.As(err, &refused) || refused.Service != 0x04 || refused.Code != 0x22 || refused.ECU != "7E8" {
		t.Errorf("negative = %v", err)
	}
	if !IsNotAvailable(err) {
		t.Error("a negative response should count as not available")
	}
	// A consecutive frame was lost.
	if _, err := ParseMessages("0902", "7E8 10 14 49 02 01 31 32 33\r7E8 22 34 35"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("lost frame = %v", err)
	}
	if _, err := ParseMessages("010C", "garbage text"); err == nil {
		t.Error("expected error for garbage")
	}
	// A single frame longer than its line is corrupt, not short data.
	if _, err := ParseMessages("010C", "7E8 06 41 0C 1A"); err == nil {
		t.Error("expected error for truncated single frame")
	}
}

func FuzzParseMessages(f *testing.F) {
	for _, seed := range []string{
		"7E8 10 14 49 02 01 31 32 33\r7E8 21 34", "014\r0: 49 02\r1: 31", "48 6B 10 43 03 01 00 00 00 00 99",
		"18DAF110 03 7F 01 11", "NO DATA", "<RX ERROR", "F:", "7E81000",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		messages, err := ParseMessages("010C", raw)
		if err == nil && len(messages) == 0 {
			t.Fatal("success without messages")
		}
		for _, m := range messages {
			if len(m.Data) == 0 {
				t.Fatal("empty message")
			}
		}
	})
}
