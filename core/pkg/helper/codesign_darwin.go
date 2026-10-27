//go:build darwin

package helper

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <bsm/libbsm.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

// checkPeer 用连接对端的 audit token 找到其代码，校验是否满足签名要求 req。
// 用 audit token 而不是 PID：PID 可被复用，对端退出后另一个进程可能拿到同一个 PID。
// 返回 0 表示满足；-1 表示取不到 audit token；其余为 Security 框架的 OSStatus。
static int checkPeer(int fd, const char *req) {
	audit_token_t token;
	socklen_t len = sizeof(token);
	if (getsockopt(fd, SOL_LOCAL, LOCAL_PEERTOKEN, &token, &len) != 0) {
		return -1;
	}
	CFDataRef data = CFDataCreate(NULL, (const UInt8 *)&token, sizeof(token));
	const void *keys[] = {kSecGuestAttributeAudit};
	const void *vals[] = {data};
	CFDictionaryRef attrs = CFDictionaryCreate(NULL, keys, vals, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFRelease(data);
	SecCodeRef code = NULL;
	OSStatus st = SecCodeCopyGuestWithAttributes(NULL, attrs, kSecCSDefaultFlags, &code);
	CFRelease(attrs);
	if (st != errSecSuccess) {
		return st;
	}
	CFStringRef s = CFStringCreateWithCString(NULL, req, kCFStringEncodingUTF8);
	SecRequirementRef r = NULL;
	st = SecRequirementCreateWithString(s, kSecCSDefaultFlags, &r);
	CFRelease(s);
	if (st == errSecSuccess) {
		st = SecCodeCheckValidity(code, kSecCSDefaultFlags, r);
		CFRelease(r);
	}
	CFRelease(code);
	return st;
}
*/
import "C"

import (
	"fmt"
	"net"
	"unsafe"
)

// TeamID 是允许连接 helper 的代码签名团队，发布构建时经 -ldflags -X 写入（与 App 的签名身份一致）。
// 为空时（本地 ad-hoc 签名的构建）只按 uid 鉴权。
var TeamID string

// PeerRequirement 返回对端必须满足的代码签名要求；TeamID 为空时返回空（不校验签名）。
func PeerRequirement() string {
	if TeamID == "" {
		return ""
	}
	return fmt.Sprintf(`anchor apple generic and certificate leaf[subject.OU] = "%s"`, TeamID)
}

// verifyPeerCode 校验连接对端进程的代码签名满足 req。
func verifyPeerCode(conn *net.UnixConn, req string) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	creq := C.CString(req)
	defer C.free(unsafe.Pointer(creq))
	var st C.int
	if err := raw.Control(func(fd uintptr) { st = C.checkPeer(C.int(fd), creq) }); err != nil {
		return err
	}
	switch st {
	case 0:
		return nil
	case -1:
		return fmt.Errorf("取不到对端 audit token")
	}
	return fmt.Errorf("代码签名不满足要求（OSStatus %d）", int(st))
}
