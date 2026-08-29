package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestATIHasNoResetSideEffect(t *testing.T) {
	s := NewELM327Simulator()
	if response, _ := s.HandleAT("ATE0"); response != "ATE0\rOK\r" {
		t.Fatalf("ATE0 response = %q", response)
	}
	response, _ := s.HandleAT("ATI")
	if response != "ELM327 v1.5\r" {
		t.Fatalf("ATI response = %q", response)
	}
	if s.echo {
		t.Fatal("ATI unexpectedly reset echo")
	}
}

func TestUnknownAndInvalidATCommands(t *testing.T) {
	s := NewELM327Simulator()
	for _, command := range []string{"ATMADEUP", "ATE9", "ATSP06", "ATSHXYZ"} {
		response, handled := s.HandleAT(command)
		if !handled || !strings.Contains(response, "?\r") {
			t.Fatalf("%s response = %q, handled=%v", command, response, handled)
		}
	}
}

func TestSupportedPIDRangeBits(t *testing.T) {
	s := NewELM327Simulator()
	s.echo = false
	s.auto = false
	response, _ := s.HandlePID("0100")
	if !hasNextRangeBit(response) {
		t.Fatalf("0100 does not advertise PID 20: %q", response)
	}
	response, _ = s.HandlePID("0120")
	if !hasNextRangeBit(response) {
		t.Fatalf("0120 does not advertise PID 40: %q", response)
	}
}

func hasNextRangeBit(response string) bool {
	fields := strings.Fields(response)
	if len(fields) < 6 {
		return false
	}
	last, err := strconv.ParseUint(fields[len(fields)-1], 16, 8)
	return err == nil && last&1 != 0
}

func TestFormattingModes(t *testing.T) {
	s := NewELM327Simulator()
	for _, command := range []string{"ATE0", "ATSP6", "ATH1", "ATS0"} {
		if response, _ := s.HandleAT(command); !strings.Contains(response, "OK") {
			t.Fatalf("%s response = %q", command, response)
		}
	}
	response, _ := s.HandlePID("010C")
	if !strings.HasPrefix(response, "7E804410C") {
		t.Fatalf("header/compact response = %q", response)
	}

	s.HandleAT("ATCAF0")
	response, _ = s.HandlePID("010D")
	if !strings.HasPrefix(response, "7E803410D") {
		t.Fatalf("CAF0 response = %q", response)
	}
}

func TestTransmitHeaderSelectsResponseAddress(t *testing.T) {
	s := NewELM327Simulator()
	s.echo, s.auto, s.headers = false, false, true
	s.HandleAT("ATSH7E2")
	response, _ := s.HandlePID("010D")
	if !strings.HasPrefix(response, "7EA ") {
		t.Fatalf("11-bit response header = %q", response)
	}
	s.HandleAT("ATSH18DA10F1")
	response, _ = s.HandlePID("010D")
	if !strings.HasPrefix(response, "18DAF110 ") {
		t.Fatalf("29-bit response header = %q", response)
	}
}

func TestProfilesLoadAndDriveResponses(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("profiles", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("profile glob: %v, paths=%v", err, paths)
	}
	for _, path := range paths {
		profile, err := loadProfile(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		s := NewELM327SimulatorWithProfile(time.Now(), profile)
		s.echo = false
		s.auto = false
		response, handled := s.HandlePID("010D")
		if !handled || !strings.HasPrefix(response, "41 0D ") {
			t.Fatalf("profile %s speed response = %q", path, response)
		}
	}
}
