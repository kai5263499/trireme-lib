package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/aporeto-inc/trireme"
	"github.com/aporeto-inc/trireme/monitor"
	"github.com/aporeto-inc/trireme/example/common"
	"github.com/pkg/profile"
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: example -stderrthreshold=[INFO|WARN|FATAL] -log_dir=[string]\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {

	defer profile.Start(profile.CPUProfile).Stop()

	flag.Usage = usage

	usePKI := *flag.Bool("pki", false, "Use PKI trireme")
	certFile := *flag.String("certFile", "cert.pem", "Set the path of certificate.")
	keyFile := *flag.String("keyFile", "key.pem", "Set the path of key certificate key to use.")
	caCertFile := *flag.String("caCertFile", "ca.crt", "Set the path of certificate authority to use.")

	flag.Parse()

	var t trireme.Trireme
	var m monitor.Monitor

	if usePKI {
		t, m = common.TriremeWithPKI(keyFile, certFile, caCertFile)
	} else {
		t, m = common.TriremeWithPSK()
	}

	if t == nil {
		panic("Failed to create Trireme")
	}

	if m == nil {
		panic("Failed to create Monitor")
	}

	t.Start()
	m.Start()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	<-c

	fmt.Println("Bye!")
	m.Stop()
	t.Stop()
}
