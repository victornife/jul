// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package wasmtest assembles minimal WebAssembly modules for tests that must
// control guest structure exactly, without a guest toolchain.
package wasmtest

// StartLoopV2 returns a valid jul-abi/v2 middleware module whose WebAssembly
// start function never terminates. It imports read_request_body and exports
// the v2 marker and handle_request, so ABI negotiation accepts it and reports
// a body-reading plugin, but any instantiation runs the start function and
// spins until its context ends.
func StartLoopV2() []byte {
	const (
		i32       = 0x7f
		funcType  = 0x60
		funcKind  = 0x00
		loopVoid  = 0x40
		opLoop    = 0x03
		opBr      = 0x0c
		opEnd     = 0x0b
		opI32Cnst = 0x41
	)
	types := vec(
		[]byte{funcType, 0, 0},                // 0: () -> ()
		[]byte{funcType, 0, 1, i32},           // 1: () -> (i32)
		[]byte{funcType, 2, i32, i32, 1, i32}, // 2: (i32, i32) -> (i32)
	)
	imports := vec(cat(name("jul-abi/v2"), name("read_request_body"), []byte{funcKind, 2})) // func 0
	funcs := vec([]byte{0}, []byte{0}, []byte{1})                                           // 1: start, 2: marker, 3: handle_request
	exports := vec(
		cat(name("jul-abi/v2"), []byte{funcKind, 2}),
		cat(name("handle_request"), []byte{funcKind, 3}),
	)
	code := vec(
		body(opLoop, loopVoid, opBr, 0, opEnd, opEnd),
		body(opEnd),
		body(opI32Cnst, 0, opEnd),
	)
	return cat(
		[]byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00},
		section(1, types),
		section(2, imports),
		section(3, funcs),
		section(7, exports),
		section(8, uleb(1)),
		section(10, code),
	)
}

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func vec(items ...[]byte) []byte { return cat(append([][]byte{uleb(uint32(len(items)))}, items...)...) }

func name(s string) []byte { return cat(uleb(uint32(len(s))), []byte(s)) }

func section(id byte, content []byte) []byte {
	return cat([]byte{id}, uleb(uint32(len(content))), content)
}

// body encodes a function body with no locals.
func body(instrs ...byte) []byte {
	b := cat([]byte{0}, instrs)
	return cat(uleb(uint32(len(b))), b)
}
