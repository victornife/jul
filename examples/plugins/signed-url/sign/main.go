// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// sign generates a link without placing key material in process arguments.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"juliaplugins/signed-url/policy"
	"os"
	"time"
)

func run(args []string, out, stderr io.Writer, getenv func(string) string, now func() time.Time) int {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("path", "/media/demo.txt", "canonical escaped resource path")
	kid := flags.String("kid", "current", "key ID")
	ttl := flags.Int64("ttl", 300, "link lifetime in seconds (1–86400)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || *ttl < 1 || *ttl > 86400 {
		fmt.Fprintln(stderr, "provide flags only; ttl must be 1–86400 seconds")
		return 1
	}
	raw, _ := json.Marshal(map[string]string{"key." + *kid: getenv("SIGNED_URL_KEY")})
	p, err := policy.Parse(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	link, err := p.Sign(*path, *kid, now().Unix()+*ttl)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := fmt.Fprintln(out, link); err != nil {
		fmt.Fprintln(stderr, "cannot write signed link")
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, time.Now)) }
