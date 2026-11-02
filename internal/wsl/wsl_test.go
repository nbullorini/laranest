package wsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetINIValue(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty file", "", "[general]\ninstanceIdleTimeout=-1\n"},
		{"other sections kept", "[wsl2]\nmemory=8GB # keep\n", "[wsl2]\nmemory=8GB # keep\n\n[general]\ninstanceIdleTimeout=-1\n"},
		{"existing section", "[General]\ndistributionInstallPath=D:\\\\wsl\n[wsl2]\nmemory=8GB\n",
			"[General]\ninstanceIdleTimeout=-1\ndistributionInstallPath=D:\\\\wsl\n[wsl2]\nmemory=8GB\n"},
		{"replaces value", "[general]\nInstanceIdleTimeout = 15000\n", "[general]\ninstanceIdleTimeout=-1\n"},
		{"crlf kept", "[wsl2]\r\nmemory=8GB\r\n", "[wsl2]\r\nmemory=8GB\r\n\r\n[general]\r\ninstanceIdleTimeout=-1\r\n"},
	}
	for _, c := range cases {
		if got := setINIValue(c.in, "general", "instanceIdleTimeout", "-1"); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	// Reverting what we added restores the original file.
	orig := "[wsl2]\r\nmemory=8GB\r\n"
	added := setINIValue(orig, "general", "instanceIdleTimeout", "-1")
	if got := setINIValue(added, "general", "instanceIdleTimeout", ""); got != orig {
		t.Errorf("revert: got %q, want %q", got, orig)
	}
	// Removing only drops our line.
	if got := setINIValue("[general]\ninstanceIdleTimeout=-1\nx=1\n", "general", "instanceIdleTimeout", ""); got != "[general]\nx=1\n" {
		t.Errorf("remove: got %q", got)
	}
}

func TestKeepAliveAndAutostart(t *testing.T) {
	profile := t.TempDir()
	env := Env{Distro: "Ubuntu", Profile: profile}
	cfg := filepath.Join(profile, ".wslconfig")
	os.WriteFile(cfg, []byte("[wsl2]\nmemory=8GB\n"), 0o644)

	if err := env.SetKeepAlive(true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg)
	if iniValue(string(data), "general", "instanceIdleTimeout") != "-1" || !strings.Contains(string(data), "memory=8GB") {
		t.Errorf("unexpected .wslconfig: %q", data)
	}
	if bak, _ := os.ReadFile(cfg + ".laranest.bak"); string(bak) != "[wsl2]\nmemory=8GB\n" {
		t.Errorf("backup: %q", bak)
	}

	if err := env.SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	script, _ := os.ReadFile(env.startupScript())
	if !strings.Contains(string(script), `wsl.exe -d ""Ubuntu"" --exec /bin/true`) {
		t.Errorf("startup script: %q", script)
	}
	if err := env.SetAutostart(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.startupScript()); !os.IsNotExist(err) {
		t.Error("startup script not removed")
	}
}

func TestWindowsPath(t *testing.T) {
	if got := WindowsPath("/mnt/c/Users/nbull/.wslconfig"); got != `C:\Users\nbull\.wslconfig` {
		t.Errorf("got %s", got)
	}
}

func TestListeningPorts(t *testing.T) {
	netstat := `
Conexiones activas

  Proto  Dirección local          Dirección remota        Estado           PID
  TCP    0.0.0.0:135            0.0.0.0:0              ESCUCHANDO       2272
  TCP    127.0.0.1:9150         0.0.0.0:0              LISTENING       17732
  TCP    127.0.0.1:80           0.0.0.0:0              LISTENING       10892
  TCP    127.0.0.1:80           0.0.0.0:0              LISTENING       17732
  TCP    127.0.0.1:9151         0.0.0.0:0              LISTENING       37260
  TCP    192.168.1.5:7000       0.0.0.0:0              LISTENING       4
  TCP    127.0.0.1:52000        127.0.0.1:9151         ESTABLISHED     900
  TCP    [::1]:8443             [::]:0                 LISTENING       37260
  TCP    [::]:445               [::]:0                 LISTENING       4
`
	got := listeningPorts(netstat, csvPIDs("\"wslrelay.exe\",\"17732\",\"Console\",\"1\",\"14,072 KB\"\r\n", "wslrelay.exe"))
	for _, p := range []int{135, 9151, 8443, 445} {
		if !got[p] {
			t.Errorf("port %d should be in use", p)
		}
	}
	for _, p := range []int{9150, 80, 7000, 52000} {
		if got[p] {
			t.Errorf("port %d should not count (relayed to WSL, LAN address or a client socket)", p)
		}
	}
}
