// synk-relay is a self-hostable WebSocket relay for Synk.
//
// Usage:
//
//	synk-relay -addr :7430
//	synk-relay -addr :7430 -cert cert.pem -key key.pem
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bristlecone-zSuite/zSynk/relay"
)

func main() {
	addr := flag.String("addr", ":7430", "listen address")
	certFile := flag.String("cert", "", "TLS certificate file (optional)")
	keyFile := flag.String("key", "", "TLS key file (optional)")
	flag.Parse()

	srv := relay.NewServer()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	var err error
	go func() {
		<-sig
		log.Println("relay: shutting down")
		os.Exit(0)
	}()

	if *certFile != "" && *keyFile != "" {
		err = srv.ListenAndServeTLS(*addr, *certFile, *keyFile)
	} else {
		err = srv.ListenAndServe(*addr)
	}

	if err != nil {
		log.Fatalf("relay: %v", err)
	}
}
