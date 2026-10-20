//! Windows：velumd 的 Named Pipe 命名、服务端身份校验、当前用户 SID、I/O 取消。
//!
//! Pipe 名字是全局的，任何进程都可以抢先创建同名 Pipe 冒充 velumd，因此连接后检查 Pipe 对象的所有者：
//! 只接受 SYSTEM / Administrators（Velum 服务）或当前用户（以用户身份运行的 velumd）。与 core/pkg/ipc 一致。

use std::ffi::c_void;
use std::fs::File;
use std::os::windows::io::AsRawHandle;
use std::ptr::null_mut;

use windows_sys::Win32::Foundation::{CloseHandle, LocalFree, HANDLE};
use windows_sys::Win32::Security::Authorization::{ConvertSidToStringSidW, GetSecurityInfo, SE_KERNEL_OBJECT};
use windows_sys::Win32::Security::{
    EqualSid, GetTokenInformation, IsWellKnownSid, TokenUser, WinBuiltinAdministratorsSid, WinLocalSystemSid, OWNER_SECURITY_INFORMATION,
    PSECURITY_DESCRIPTOR, PSID, TOKEN_QUERY, TOKEN_USER,
};
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};
use windows_sys::Win32::System::IO::CancelIoEx;

/// Velum 服务（全机一个）的 Pipe。
pub const SERVICE_PIPE: &str = r"\\.\pipe\velumd";

/// 以当前用户身份运行的 velumd 的 Pipe（按用户区分，与 velumd 的 config.UserPipe 一致）。
pub fn user_pipe() -> Result<String, String> {
    Ok(format!("{SERVICE_PIPE}-{}", current_sid()?))
}

/// 当前进程用户的 TOKEN_USER（按 8 字节对齐的缓冲区，其中的 SID 指针指向缓冲区内部）。
fn current_user() -> Result<Vec<u64>, String> {
    // SAFETY：令牌句柄用完即关闭；缓冲区长度由第一次调用给出。
    unsafe {
        let mut tok: HANDLE = null_mut();
        if OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut tok) == 0 {
            return Err(format!("读取当前用户：{}", std::io::Error::last_os_error()));
        }
        let mut len = 0u32;
        GetTokenInformation(tok, TokenUser, null_mut(), 0, &mut len);
        let mut buf = vec![0u64; (len as usize).div_ceil(8)];
        let ok = GetTokenInformation(tok, TokenUser, buf.as_mut_ptr().cast::<c_void>(), len, &mut len);
        let err = std::io::Error::last_os_error();
        CloseHandle(tok);
        if ok == 0 {
            return Err(format!("读取当前用户：{err}"));
        }
        Ok(buf)
    }
}

fn sid_of(user: &[u64]) -> PSID {
    // SAFETY：user 是 GetTokenInformation(TokenUser) 填充的 TOKEN_USER。
    unsafe { (*user.as_ptr().cast::<TOKEN_USER>()).User.Sid }
}

/// 当前用户的 SID 字符串（如 S-1-5-21-…）。
pub fn current_sid() -> Result<String, String> {
    let user = current_user()?;
    // SAFETY：ConvertSidToStringSidW 分配的字符串以 LocalFree 释放。
    unsafe {
        let mut s: *mut u16 = null_mut();
        if ConvertSidToStringSidW(sid_of(&user), &mut s) == 0 {
            return Err(format!("读取当前用户：{}", std::io::Error::last_os_error()));
        }
        let len = (0..).take_while(|&i| *s.add(i) != 0).count();
        let out = String::from_utf16_lossy(std::slice::from_raw_parts(s, len));
        LocalFree(s.cast());
        Ok(out)
    }
}

/// 检查 Pipe 对象的所有者是否可信。
pub fn verify(f: &File) -> Result<(), String> {
    let user = current_user().ok();
    // SAFETY：owner 指向 sd 内部，sd 以 LocalFree 释放；比较在释放前完成。
    unsafe {
        let mut owner: PSID = null_mut();
        let mut sd: PSECURITY_DESCRIPTOR = null_mut();
        let rc = GetSecurityInfo(f.as_raw_handle() as HANDLE, SE_KERNEL_OBJECT, OWNER_SECURITY_INFORMATION, &mut owner, null_mut(), null_mut(), null_mut(), &mut sd);
        if rc != 0 {
            return Err(format!("读取 Pipe 所有者失败（{rc}）"));
        }
        let trusted = IsWellKnownSid(owner, WinLocalSystemSid) != 0
            || IsWellKnownSid(owner, WinBuiltinAdministratorsSid) != 0
            || user.as_ref().is_some_and(|u| EqualSid(owner, sid_of(u)) != 0);
        LocalFree(sd);
        if trusted {
            Ok(())
        } else {
            Err("velumd 的 Pipe 所有者不可信（可能被其他程序冒充）".into())
        }
    }
}

/// 取消该句柄上挂起的 I/O（同步 Pipe 不支持读超时，超时后由看门狗调用）。
pub fn cancel_io(handle: usize) {
    // SAFETY：调用方保证句柄在调用期间有效。
    unsafe {
        CancelIoEx(handle as HANDLE, std::ptr::null());
    }
}
