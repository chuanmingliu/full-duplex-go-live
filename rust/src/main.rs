use anyhow::{Context, Result, bail};
use golive_rs::{config::Config, provider, server};
use serde_json::json;
use std::path::PathBuf;
use tokio_util::sync::CancellationToken;

#[tokio::main]
async fn main() -> Result<()> {
    let mut args = std::env::args().skip(1);
    let mut profile = None;
    let mut dotenv = Some(PathBuf::from(".env.local"));
    let mut addr = None;
    let mut print = false;
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--profile" | "-profile" => {
                profile = Some(PathBuf::from(
                    args.next().context("--profile needs a path")?,
                ))
            }
            "--env" | "-env" => {
                dotenv = Some(PathBuf::from(args.next().context("--env needs a path")?))
            }
            "--addr" | "-addr" => addr = Some(args.next().context("--addr needs an address")?),
            "--print-config" | "-print-config" => print = true,
            "--version" | "-version" => {
                println!("golive-rs {}", env!("CARGO_PKG_VERSION"));
                return Ok(());
            }
            "--help" | "-h" => {
                println!(
                    "golive-rs [--profile FILE] [--env FILE] [--addr HOST:PORT] [--print-config] [--version]\nNative Rust voice service. Default: mock providers on 127.0.0.1:8766. See rust/README.md for compatibility."
                );
                return Ok(());
            }
            _ => bail!("unknown argument {arg}; use --help"),
        }
    }
    let mut cfg = Config::load(profile.as_deref(), dotenv.as_deref())?;
    if let Some(addr) = addr {
        cfg.set("addr", json!(addr));
    }
    if print {
        println!("{}", serde_json::to_string_pretty(&cfg.redacted())?);
        return Ok(());
    }
    let filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| cfg.string("log_level", "info").into());
    if cfg.string("log_format", "text") == "json" {
        tracing_subscriber::fmt()
            .with_env_filter(filter)
            .with_target(false)
            .json()
            .init();
    } else {
        tracing_subscriber::fmt()
            .with_env_filter(filter)
            .with_target(false)
            .init();
    }
    cfg.warn_compatibility();
    provider::preflight(&cfg)?;
    let shutdown = CancellationToken::new();
    let signal = shutdown.clone();
    tokio::spawn(async move {
        #[cfg(unix)]
        {
            let mut term =
                tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                    .expect("SIGTERM handler");
            tokio::select! {_=tokio::signal::ctrl_c()=>{},_=term.recv()=>{}}
        }
        #[cfg(not(unix))]
        {
            let _ = tokio::signal::ctrl_c().await;
        }
        signal.cancel();
    });
    tracing::info!(address=%cfg.string("addr","127.0.0.1:8766"),"starting Rust voice service");
    server::serve(cfg, shutdown).await
}
