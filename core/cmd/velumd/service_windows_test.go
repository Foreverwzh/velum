package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestServiceSDDL(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL("S-1-5-21-1-2-3-1001"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sd.DACL(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine(`C:\Program Files\Velum\velumd.exe`, "--home", `C:\Users\a b\AppData\Roaming\Velum`, "--allow-sid", "S-1-5-21-1")
	want := `"C:\Program Files\Velum\velumd.exe" --home "C:\Users\a b\AppData\Roaming\Velum" --allow-sid S-1-5-21-1`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}
