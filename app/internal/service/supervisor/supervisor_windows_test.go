//go:build windows

package supervisor

import "testing"

func TestParseWMIC(t *testing.T) {
	out := []byte("Node,ExecutablePath,ProcessId\r\n" +
		"DESKTOP,\"C:\\Program Files\\sing-box\\sing-box.exe\",1234\r\n" +
		"DESKTOP,\"C:\\tools,a\\sing-box.exe\",5678\r\n" +
		"DESKTOP,,9999\r\n" +
		"DESKTOP,\"C:\\x\\sing-box.exe\",oops\r\n")
	procs := parseWMIC(out)
	if len(procs) != 2 {
		t.Fatalf("procs = %d, want 2: %+v", len(procs), procs)
	}
	if procs[0].Path != `C:\Program Files\sing-box\sing-box.exe` || procs[0].PID != 1234 {
		t.Fatalf("procs[0] = %+v", procs[0])
	}
	if procs[1].Path != `C:\tools,a\sing-box.exe` || procs[1].PID != 5678 {
		t.Fatalf("procs[1] = %+v", procs[1])
	}
}

func TestParsePowerShell(t *testing.T) {
	out := []byte("1234|C:\\Program Files\\sing-box\\sing-box.exe\r\n" +
		"5678|C:\\tools\\sing-box.exe\r\n" +
		"nope|C:\\bad.exe\r\n" +
		"9012|\r\n" +
		"broken-line\r\n")
	procs := parsePowerShell(out)
	if len(procs) != 2 {
		t.Fatalf("procs = %d, want 2: %+v", len(procs), procs)
	}
	if procs[0].Path != `C:\Program Files\sing-box\sing-box.exe` || procs[0].PID != 1234 {
		t.Fatalf("procs[0] = %+v", procs[0])
	}
	if procs[1].Path != `C:\tools\sing-box.exe` || procs[1].PID != 5678 {
		t.Fatalf("procs[1] = %+v", procs[1])
	}
}
