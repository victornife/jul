// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// ExitCode is one row of ADR 0019 §33's stable automation exit contract.
// The table is shared by the server capabilities endpoint and the local
// `jul capabilities` command so downstream clients never maintain a second
// mapping.
type ExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

var exitCodeContract = []ExitCode{
	{Code: 0, Meaning: "success: applied live, read completed, clean shutdown, or healthy probe"},
	{Code: 1, Meaning: "validation or configuration error"},
	{Code: 2, Meaning: "usage error: bad flags, missing argument, disabled admin, malformed request, or missing resource"},
	{Code: 3, Meaning: "success; restart required to converge (staged or owned_not_serving)"},
	{Code: 4, Meaning: "success with a degraded outcome"},
	{Code: 5, Meaning: "conflict or state left uncertain by failed restoration"},
	{Code: 6, Meaning: "configuration authority denial: server is file_owned"},
	{Code: 7, Meaning: "authentication, authorization, or insecure-transport refusal"},
	{Code: 8, Meaning: "connectivity, TLS, or rate-limit failure"},
	{Code: 9, Meaning: "server, capability, storage, timeout, or internal failure"},
}

// ExitCodes returns a defensive copy of the stable exit-code table.
func ExitCodes() []ExitCode {
	out := make([]ExitCode, len(exitCodeContract))
	copy(out, exitCodeContract)
	return out
}
