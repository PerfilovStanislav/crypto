//! Заданные обороты вентиляторов на время перебора: `fan set <rpm>`, по завершении `fan auto`
//! (утилита https://github.com/SirGian99/mac-fan-control, нужен root).
//!
//! Запускается один процесс `sudo sh -c …`: он задаёт обороты и ждёт закрытия своего stdin,
//! после чего сам возвращает автоматический режим. stdin закрывается при любом завершении
//! анализатора — обычном, с ошибкой, по Ctrl+C и даже при `kill -9`, — так что вентиляторы не
//! останутся на заданных оборотах и второй раз sudo не нужен. Пароль sudo спрашивается в терминале
//! один раз, в начале.

use std::io::{BufRead, BufReader};
use std::process::{Child, ChildStdin, Command, Stdio};

use anyhow::{bail, Context, Result};

/// Ctrl+C приходит всей группе процессов — root-процесс его игнорирует, чтобы успеть
/// выполнить `fan auto`, когда анализатор завершится.
fn script(rpm: u32) -> String {
    format!(
        "trap '' INT TERM HUP; fan set {rpm} >/dev/null || exit 1; echo ready; exec >/dev/null; read _; fan auto"
    )
}

pub struct FanBoost {
    child: Child,
    stdin: Option<ChildStdin>,
}

impl FanBoost {
    /// Задаёт `rpm` оборотов всем вентиляторам и ждёт подтверждения (в том числе ввода
    /// пароля sudo).
    pub fn start(rpm: u32) -> Result<Self> {
        let mut child = Command::new("sudo")
            .args(["sh", "-c", &script(rpm)])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .context("failed to run sudo")?;
        let stdin = child.stdin.take();

        let mut line = String::new();
        if let Some(stdout) = child.stdout.take() {
            BufReader::new(stdout).read_line(&mut line)?;
        }
        if line.trim() != "ready" {
            drop(stdin);
            let status = child.wait()?;
            bail!("`sudo fan set {rpm}` failed ({status})");
        }
        Ok(Self { child, stdin })
    }
}

impl Drop for FanBoost {
    fn drop(&mut self) {
        // EOF на stdin — сигнал root-процессу выполнить `fan auto`; ждём, пока он закончит.
        drop(self.stdin.take());
        let _ = self.child.wait();
    }
}
