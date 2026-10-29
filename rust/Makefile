.PHONY: check test build run
check:
	cargo fmt --check
	cargo clippy --locked --all-targets -- -D warnings
test:
	cargo test --locked
build:
	cargo build --locked --release
run:
	cargo run --locked -- --addr 127.0.0.1:8766
