package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	spec := flag.String("h2spec", "h2spec", "h2spec executable built from v2.6.0")
	out := flag.String("out", "h2stdlib-results", "report directory")
	maxFrame := flag.Int("max-frame", 0, "supported HTTP2Config.MaxReadFrameSize override")
	implementation := flag.String("implementation", "stdlib", "server implementation")
	flag.Parse()
	if *implementation != "stdlib" && *implementation != "xnet" {
		panic("implementation must be stdlib or xnet")
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		panic(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	tlsServer := httptest.NewUnstartedServer(handler)
	tlsServer.EnableHTTP2 = true
	tlsServer.Config.HTTP2 = &http.HTTP2Config{MaxReadFrameSize: *maxFrame}
	if *implementation == "xnet" {
		if err := http2.ConfigureServer(tlsServer.Config, &http2.Server{MaxReadFrameSize: uint32(*maxFrame)}); err != nil {
			panic(err)
		}
	}
	tlsServer.StartTLS()
	defer tlsServer.Close()
	plainServer := httptest.NewUnstartedServer(handler)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	plainServer.Config.Protocols = &protocols
	plainServer.Config.HTTP2 = &http.HTTP2Config{MaxReadFrameSize: *maxFrame}
	if *implementation == "xnet" {
		plainServer.Config.Protocols = nil
		plainServer.Config.Handler = h2c.NewHandler(handler, &http2.Server{MaxReadFrameSize: uint32(*maxFrame)})
	}
	plainServer.Start()
	defer plainServer.Close()
	for _, target := range []struct {
		name, url string
		tls       bool
	}{
		{"tls-static", tlsServer.URL, true}, {"tls-proxy", tlsServer.URL, true},
		{"h2c-static", plainServer.URL, false}, {"h2c-proxy", plainServer.URL, false},
	} {
		_, port, err := net.SplitHostPort(strings.SplitN(target.url, "://", 2)[1])
		if err != nil {
			panic(err)
		}
		args := []string{"-h", "127.0.0.1", "-p", port, "-o", "5", "-j", filepath.Join(*out, "h2spec-"+target.name+".xml")}
		if target.tls {
			args = append(args, "-t", "-k")
		}
		if strings.HasSuffix(target.name, "proxy") {
			args = append(args, "-P", "/proxy/")
		}
		log, err := os.Create(filepath.Join(*out, "h2spec-"+target.name+".txt"))
		if err != nil {
			panic(err)
		}
		command := exec.Command(*spec, args...)
		command.Stdout, command.Stderr = log, log
		err = command.Run()
		_ = log.Close()
		fmt.Printf("%s: %v\n", target.name, err)
	}
}
