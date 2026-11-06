package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const socketPath = "unix:///run/spire/sockets/agent.sock"

func main() {
	ctx := context.Background()

	// Same as the server: get our SVID and the bundle from the a
	source, err := workloadapi.NewX509Source(ctx,
		workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	if err != nil {
		log.Fatalf("unable to create X509Source: %v", err)
	}
	defer source.Close()

	// The client checks the server too: it must be service-b.
	serverID := spiffeid.RequireFromString(os.Getenv("SERVER_ID"))
	tlsConfig := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(serverID))

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   5 * time.Second,
	}
	url := os.Getenv("SERVER_URL")

	for {
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("request failed: %v", err)
		} else {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Printf("read failed: %v", err)
			}
			resp.Body.Close()
			log.Printf("%s %s", resp.Status, body)
		}
		time.Sleep(5 * time.Second)
	}
}
