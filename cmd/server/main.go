package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const socketPath = "unix:///run/spire/sockets/agent.sock"

func main() {
	ctx := context.Background()

	// 1. Connect to the SPIRE agent. The source holds this pod's SVID and
	//    the trust bundle, and swaps in new ones as they rotate.
	source, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	if err != nil {
		log.Fatalf("unable to create X509Source: %v", err)
	}
	defer source.Close()

	allowed := spiffeid.RequireFromString(os.Getenv("ALLOWED_CLIENT"))

	tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(allowed))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		id, err := spiffetls.PeerIDFromConnectionState(*r.TLS)
		if err != nil {
			http.Error(w, "no SPIFFE ID", http.StatusUnauthorized)
			return
		}

		log.Printf("request from %s", id)
		fmt.Fprintf(w, "hello %s\n", id)
	})

	server := &http.Server{Addr: ":8443", TLSConfig: tlsConfig}
	log.Fatal(server.ListenAndServeTLS("", ""))
}
