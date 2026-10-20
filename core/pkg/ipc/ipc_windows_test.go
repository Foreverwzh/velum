//go:build windows

package ipc

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPipeSDDL(t *testing.T) {
	got, err := PipeSDDL([]string{"S-1-5-21-1-2-3-1001"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;S-1-5-21-1-2-3-1001)"; got != want {
		t.Fatalf("got %s", got)
	}
	if _, err := PipeSDDL([]string{"not-a-sid"}); err == nil {
		t.Fatal("应拒绝无效 SID")
	}
}

func TestTrustedOwner(t *testing.T) {
	sys, _ := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	admins, _ := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	users, _ := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	u, _ := windows.GetCurrentProcessToken().GetTokenUser()
	for _, c := range []struct {
		sid  *windows.SID
		want bool
	}{{sys, true}, {admins, true}, {u.User.Sid, true}, {users, false}} {
		if TrustedOwner(c.sid) != c.want {
			t.Errorf("%s：期望 %v", c.sid, c.want)
		}
	}
}
