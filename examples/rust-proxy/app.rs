// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Minimal Rust HTTP app served behind Jul via proxy_pass.
//
// Dependency-free: uses only the Rust standard library, so no crates are
// needed. Each connection is handled on its own thread, which keeps it
// responsive behind Jul's reverse proxy (which reuses keep-alive connections).
//
// Build and run with Cargo (from this folder):
//     cd examples/rust-proxy
//     cargo run --release
//
// Or compile the single file directly with rustc (no Cargo needed):
//     rustc -O examples/rust-proxy/app.rs -o examples/rust-proxy/app
//     ./examples/rust-proxy/app           (Linux/macOS)
//     .\examples\rust-proxy\app.exe        (Windows)

use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream};
use std::thread;

const ADDR: &str = "127.0.0.1:3034";

fn main() {
    let listener = TcpListener::bind(ADDR).expect("failed to bind");
    println!("Serving on http://{ADDR} (Ctrl+C to stop)");
    for stream in listener.incoming() {
        match stream {
            Ok(stream) => {
                thread::spawn(move || handle(stream));
            }
            Err(e) => eprintln!("accept error: {e}"),
        }
    }
}

fn handle(mut stream: TcpStream) {
    // Read and discard the request head (up to the blank line). We respond the
    // same way regardless of the request, and close the connection after.
    let mut reader = BufReader::new(match stream.try_clone() {
        Ok(s) => s,
        Err(_) => return,
    });
    let mut line = String::new();
    loop {
        line.clear();
        match reader.read_line(&mut line) {
            Ok(0) => return, // connection closed
            Ok(_) => {
                if line == "\r\n" || line == "\n" {
                    break; // end of headers
                }
            }
            Err(_) => return,
        }
    }

    let body = "Hello from a Rust app behind Jul (proxy_pass over HTTP)!\n";
    let response = format!(
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/plain; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n\
         {}",
        body.len(),
        body
    );
    let _ = stream.write_all(response.as_bytes());
    let _ = stream.flush();
}
