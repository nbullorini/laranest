package trust

import "testing"

func TestNSSToolInstall(t *testing.T) {
	cases := map[string]string{
		"ID=ubuntu\nID_LIKE=debian\n": "sudo apt install libnss3-tools",
		"ID=debian\n":                 "sudo apt install libnss3-tools",
		`ID="rocky"` + "\n" + `ID_LIKE="rhel centos fedora"`: "sudo dnf install nss-tools",
		"ID=fedora\n": "sudo dnf install nss-tools",
		"ID=arch\n":   "sudo pacman -S nss",
		`ID="opensuse-tumbleweed"` + "\nID_LIKE=\"opensuse suse\"\n": "sudo zypper install mozilla-nss-tools",
	}
	for osRelease, want := range cases {
		if got := nssToolInstall(osRelease); got != want {
			t.Errorf("%q: got %q, want %q", osRelease, got, want)
		}
	}
}
