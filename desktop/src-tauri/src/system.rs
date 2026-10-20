//! 需要系统权限或系统集成的操作：安装 / 卸载 TUN 组件、开机启动。各平台实现相同的函数。

#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
pub use macos::*;

#[cfg(windows)]
mod windows;
#[cfg(windows)]
pub use windows::*;
