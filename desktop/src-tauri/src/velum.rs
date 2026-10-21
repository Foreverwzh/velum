//! velumd 本地接口客户端：HTTP/1.1 over Unix socket（macOS）/ Named Pipe（Windows），支持 Content-Length 与 chunked。
//! 桌面壳不含业务逻辑，所有能力都经此访问 velumd。

use std::io::{BufRead, BufReader, Read, Write};
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// Velum 目录：$VELUM_HOME，否则 macOS ~/Library/Application Support/Velum、Windows %APPDATA%\Velum（与 velumd 一致）。
pub fn home() -> PathBuf {
    if let Ok(h) = std::env::var("VELUM_HOME") {
        return PathBuf::from(h);
    }
    #[cfg(windows)]
    {
        std::env::var("APPDATA")
            .map(PathBuf::from)
            .unwrap_or_default()
            .join("Velum")
    }
    #[cfg(not(windows))]
    {
        std::env::var("HOME")
            .map(PathBuf::from)
            .unwrap_or_default()
            .join("Library/Application Support/Velum")
    }
}

#[cfg(unix)]
type Conn = std::os::unix::net::UnixStream;

#[cfg(unix)]
fn connect(timeout: Option<Duration>) -> Result<Conn, String> {
    let s =
        Conn::connect(home().join("velumd.sock")).map_err(|e| format!("无法连接 velumd：{e}"))?;
    s.set_read_timeout(timeout).ok();
    s.set_write_timeout(Some(Duration::from_secs(10))).ok();
    Ok(s)
}

#[cfg(windows)]
type Conn = std::fs::File;

/// Windows：依次尝试本用户的 Pipe（以用户身份运行的 velumd）与 Velum 服务的 Pipe，连接后校验服务端身份。
/// 所有实例都忙（ERROR_PIPE_BUSY）时稍后重试。同步 Pipe 不支持读超时，超时由 with_deadline 处理。
#[cfg(windows)]
fn connect(_timeout: Option<Duration>) -> Result<Conn, String> {
    use crate::winpipe;
    const ERROR_FILE_NOT_FOUND: i32 = 2;
    const ERROR_PIPE_BUSY: i32 = 231;
    let mut first: Option<String> = None;
    for name in [winpipe::user_pipe()?, winpipe::SERVICE_PIPE.to_string()] {
        let mut opened = None;
        for _ in 0..20 {
            match std::fs::OpenOptions::new()
                .read(true)
                .write(true)
                .open(&name)
            {
                Ok(f) => {
                    opened = Some(Ok(f));
                    break;
                }
                Err(e) if e.raw_os_error() == Some(ERROR_PIPE_BUSY) => {
                    std::thread::sleep(Duration::from_millis(50))
                }
                Err(e) => {
                    opened = Some(Err(e));
                    break;
                }
            }
        }
        let err = match opened {
            Some(Ok(f)) => match winpipe::verify(&f) {
                Ok(()) => return Ok(f),
                Err(e) => e,
            },
            Some(Err(e)) if e.raw_os_error() == Some(ERROR_FILE_NOT_FOUND) => {
                "velumd 未运行".to_string()
            }
            Some(Err(e)) => e.to_string(),
            None => "所有连接都忙".to_string(),
        };
        first.get_or_insert(err);
    }
    Err(format!("无法连接 velumd：{}", first.unwrap_or_default()))
}

/// 在限定时间内完成一次请求。Unix socket 已设置读写超时；Windows 的同步 Pipe 没有读超时，
/// 由看门狗线程在超时后取消挂起的 I/O，避免 velumd 无响应时界面与托盘线程永久阻塞。
/// Unix 上由 socket 读超时（connect 时设置）中断；超时报出的是 EAGAIN（os error 35 / 11），这里换成可读的说明。
#[cfg(unix)]
fn with_deadline<T>(
    s: &mut Conn,
    t: Duration,
    f: impl FnOnce(&mut Conn) -> Result<T, String>,
) -> Result<T, String> {
    let start = Instant::now();
    let r = f(s);
    if r.is_err() && start.elapsed() + Duration::from_secs(1) >= t {
        return Err(timeout_msg(t));
    }
    r
}

fn timeout_msg(t: Duration) -> String {
    format!(
        "velumd 未在 {} 秒内响应（可能正在拉取订阅或规则集，稍后查看状态）",
        t.as_secs()
    )
}

#[cfg(windows)]
fn with_deadline<T>(
    s: &mut Conn,
    t: Duration,
    f: impl FnOnce(&mut Conn) -> Result<T, String>,
) -> Result<T, String> {
    use std::os::windows::io::AsRawHandle;
    let handle = s.as_raw_handle() as usize;
    let (tx, rx) = std::sync::mpsc::channel::<()>();
    let dog = std::thread::spawn(move || {
        let timed_out = rx.recv_timeout(t) == Err(std::sync::mpsc::RecvTimeoutError::Timeout);
        if timed_out {
            crate::winpipe::cancel_io(handle);
        }
        timed_out
    });
    let r = f(s);
    let _ = tx.send(());
    // 句柄在看门狗结束后才随 s 释放
    if dog.join().unwrap_or(false) && r.is_err() {
        return Err(timeout_msg(t));
    }
    r
}

fn write_request(s: &mut Conn, method: &str, path: &str, body: Option<&str>) -> Result<(), String> {
    let body = body.unwrap_or("");
    let req = format!(
        "{method} {path} HTTP/1.1\r\nHost: velumd\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    );
    s.write_all(req.as_bytes()).map_err(|e| e.to_string())
}

/// 读取状态行与头部，返回 (状态码, 是否 chunked, Content-Length)。
fn read_head<R: BufRead>(r: &mut R) -> Result<(u16, bool, Option<usize>), String> {
    let mut line = String::new();
    r.read_line(&mut line).map_err(|e| e.to_string())?;
    let code = line
        .split_whitespace()
        .nth(1)
        .and_then(|c| c.parse().ok())
        .ok_or("响应格式无效")?;
    let (mut chunked, mut len) = (false, None);
    loop {
        line.clear();
        r.read_line(&mut line).map_err(|e| e.to_string())?;
        let l = line.trim_end();
        if l.is_empty() {
            break;
        }
        if let Some((k, v)) = l.split_once(':') {
            let (k, v) = (k.trim().to_ascii_lowercase(), v.trim());
            if k == "transfer-encoding" && v.eq_ignore_ascii_case("chunked") {
                chunked = true;
            } else if k == "content-length" {
                len = v.parse().ok();
            }
        }
    }
    Ok((code, chunked, len))
}

/// chunked 解码器：按块读取，供普通响应与 SSE 流共用。
struct Chunked<R: BufRead> {
    inner: R,
    left: usize,
    done: bool,
}

impl<R: BufRead> Read for Chunked<R> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        if self.done {
            return Ok(0);
        }
        if self.left == 0 {
            let mut line = String::new();
            self.inner.read_line(&mut line)?;
            if line.trim().is_empty() {
                line.clear();
                self.inner.read_line(&mut line)?; // 上一块末尾的 CRLF
            }
            let size = usize::from_str_radix(line.trim().split(';').next().unwrap_or("0"), 16)
                .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
            if size == 0 {
                self.done = true;
                return Ok(0);
            }
            self.left = size;
        }
        let want = buf.len().min(self.left);
        let n = self.inner.read(&mut buf[..want])?;
        self.left -= n;
        Ok(n)
    }
}

/// 发送请求并返回 JSON；非 2xx 时返回 velumd 给出的错误信息。
pub fn call(method: &str, path: &str, body: Option<&str>) -> Result<serde_json::Value, String> {
    // 保存订阅、更新、一键修复等会拉取订阅与几十个规则集，网络不佳时可能需要数分钟；查询类请求应很快返回
    let timeout = Duration::from_secs(if method == "GET" { 60 } else { 180 });
    let mut s = connect(Some(timeout))?;
    let (code, data) = with_deadline(&mut s, timeout, |s| {
        write_request(s, method, path, body)?;
        let mut r = BufReader::new(s);
        let (code, chunked, len) = read_head(&mut r)?;
        let mut data = Vec::new();
        if chunked {
            Chunked {
                inner: r,
                left: 0,
                done: false,
            }
            .read_to_end(&mut data)
            .map_err(|e| e.to_string())?;
        } else if let Some(n) = len {
            data.resize(n, 0);
            r.read_exact(&mut data).map_err(|e| e.to_string())?;
        } else {
            r.read_to_end(&mut data).map_err(|e| e.to_string())?;
        }
        Ok((code, data))
    })?;
    let v: serde_json::Value = serde_json::from_slice(&data).unwrap_or(serde_json::Value::Null);
    if (200..300).contains(&code) {
        Ok(v)
    } else {
        Err(v
            .get("error")
            .and_then(|e| e.as_str())
            .map(str::to_owned)
            .unwrap_or_else(|| format!("HTTP {code}")))
    }
}

/// 订阅连接事件流（SSE），每条事件回调一次；连接断开时返回。
pub fn stream(mut on_event: impl FnMut(serde_json::Value)) -> Result<(), String> {
    let mut s = connect(None)?;
    write_request(&mut s, "GET", "/v1/connections/stream", None)?;
    let mut r = BufReader::new(s);
    let (code, chunked, _) = read_head(&mut r)?;
    if code != 200 {
        return Err(format!("HTTP {code}"));
    }
    let lines: Box<dyn BufRead> = if chunked {
        Box::new(BufReader::new(Chunked {
            inner: r,
            left: 0,
            done: false,
        }))
    } else {
        Box::new(r)
    };
    for line in lines.lines() {
        let line = line.map_err(|e| e.to_string())?;
        if let Some(data) = line.strip_prefix("data: ") {
            if let Ok(v) = serde_json::from_str(data) {
                on_event(v);
            }
        }
    }
    Ok(())
}

pub fn alive() -> bool {
    call("GET", "/v1/status", None).is_ok()
}

/// 查找 velumd：$VELUMD → 与本程序同目录（App 内携带）→ 开发目录 core/bin。
pub fn velumd_path() -> Option<PathBuf> {
    if let Ok(p) = std::env::var("VELUMD") {
        return Some(PathBuf::from(p));
    }
    bin_path("velumd")
}

/// 内置程序的路径：App 包内与主程序同目录（Contents/MacOS）；开发时回落到仓库的 core/bin。
pub fn bin_path(name: &str) -> Option<PathBuf> {
    let name = &format!("{name}{}", std::env::consts::EXE_SUFFIX);
    let exe = std::env::current_exe().ok()?;
    let dir = exe.parent()?;
    let bundled = dir.join(name);
    if bundled.exists() {
        return Some(bundled);
    }
    // 开发：desktop/src-tauri/target/debug/velum-desktop → core/bin/<name>
    for anc in dir.ancestors() {
        let p = anc.join("core/bin").join(name);
        if p.exists() {
            return Some(p);
        }
    }
    None
}

/// 确保 velumd 在运行：Windows 上已安装 Velum 服务时启动服务；否则以独立进程启动 velumd（关闭界面不影响），并等待就绪。
pub fn ensure_running() -> Result<(), String> {
    if alive() {
        return Ok(());
    }
    let bin = velumd_path().ok_or("找不到 velumd（可设置环境变量 VELUMD 指定路径）")?;
    #[cfg(windows)]
    {
        if crate::system::service_status()
            .map(|s| s["installed"].as_bool() == Some(true))
            .unwrap_or(false)
        {
            crate::system::run_hidden(&bin, &["service", "start"])?;
            return wait_ready();
        }
    }
    let logs = home().join("logs");
    std::fs::create_dir_all(&logs).ok();
    let out = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(logs.join("velumd.out"))
        .map_err(|e| e.to_string())?;
    let mut cmd = Command::new(&bin);
    cmd.arg("--home")
        .arg(home())
        .stdin(Stdio::null())
        .stdout(out.try_clone().map_err(|e| e.to_string())?)
        .stderr(out);
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        cmd.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        // 脱离 App 的控制台与进程组，不弹出窗口
        const DETACHED_PROCESS: u32 = 0x08;
        const CREATE_NEW_PROCESS_GROUP: u32 = 0x200;
        cmd.creation_flags(DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP);
    }
    cmd.spawn().map_err(|e| format!("启动 velumd 失败：{e}"))?;
    wait_ready()
}

fn wait_ready() -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if alive() {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(150));
    }
    Err("velumd 未能在 10 秒内就绪，查看 logs/velumd.log".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    // 需要运行中的 velumd：VELUM_HOME=/tmp/velum-dev cargo test -- --ignored
    #[test]
    #[ignore]
    fn call_and_stream_against_running_velumd() {
        let s = call("GET", "/v1/status", None).expect("status");
        assert!(s["pid"].as_u64().unwrap() > 0);
        // 较大的 JSON 响应（chunked）
        let w = call("GET", "/v1/warnings", None).expect("warnings");
        assert!(w["warnings"].is_array());
        // 错误信息透传
        let e = call(
            "POST",
            "/v1/groups/select",
            Some(r#"{"group":"nope","member":"x"}"#),
        )
        .unwrap_err();
        assert!(e.contains("nope"), "{e}");
        // SSE：发起一次经 velumd 的请求，应收到连接事件
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = stream(|v| {
                let _ = tx.send(v);
            });
        });
        std::thread::sleep(Duration::from_millis(300));
        let listen = s["listen"].as_str().unwrap().to_string();
        let _ = std::process::Command::new("curl")
            .args([
                "-s",
                "-m",
                "8",
                "-o",
                "/dev/null",
                "-x",
                &format!("http://{listen}"),
                "https://www.baidu.com/",
            ])
            .status();
        let ev = rx
            .recv_timeout(Duration::from_secs(10))
            .expect("应收到连接事件");
        assert!(ev["chain"].is_array(), "{ev}");
    }
}
