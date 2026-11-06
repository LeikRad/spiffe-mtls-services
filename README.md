# spiffe-mtls-services

Two small Go services that authenticate each other with SPIFFE identities instead of IP addresses or hostnames. `service-a` may call `service-b`, and `service-c` may not, even though all three are on the same network, run the same image and hold valid certificates from the same CA.
I made it to see what identity-based access control looks like in code. Once it worked, I broke it on purpose: swapped the policy, made the client distrust the server, and revoked an identity mid-run.

This builds on [mini-ca-mtls](https://github.com/LeikRad/mini-ca-mtls), where I did mTLS with hand-made certs, and [spire-kind-lab](https://github.com/LeikRad/spire-kind-lab), where I set up the SPIRE cluster these services run on.

## What's in here

| Path                          | What it is                                                                                  |
| ----------------------------- | ------------------------------------------------------------------------------------------- |
| `cmd/server/main.go`          | `service-b`. mTLS server that accepts only the SPIFFE ID in `ALLOWED_CLIENT`.               |
| `cmd/client/main.go`          | Calls `SERVER_URL` every 5s and accepts only a server whose ID is `SERVER_ID`.              |
| `Dockerfile`                  | Two-stage build. Both static binaries on `distroless/static`, about 20 MB.                  |
| `manifests/`                  | `service-a` and `service-c` run the client, `service-b` runs the server.                    |
| `scripts/register-services.sh` | Registers the three SPIFFE IDs with a 120 second SVID TTL.                                 |
| `notes/`                      | Logs captured during the experiments below.                                                 |

## Run it

Needs the cluster from [spire-kind-lab](https://github.com/LeikRad/spire-kind-lab) running, with its node alias entry registered. Also Docker, kind and kubectl.

```sh
docker build -t spiffe-demo:0.2 .
kind load docker-image spiffe-demo:0.2 --name spire-lab

bash scripts/register-services.sh
kubectl apply -f manifests/

kubectl logs -f deploy/service-b
```

`register-services.sh` is not idempotent, same as in spire-kind-lab.

To build locally, use `CGO_ENABLED=0 go build ./cmd/server ./cmd/client`.

## How it works

Neither service has a cert file, a key file or a CA file. Both start the same way:

```go
source, err := workloadapi.NewX509Source(ctx,
	workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
```

This connects to the SPIRE agent's socket and keeps the pod's current SVID and trust bundle in memory, replacing them when the agent pushes new ones. The agent decides which SVID the pod gets through workload attestation, as described in spire-kind-lab.

The server then builds its TLS config:

```go
allowed := spiffeid.RequireFromString(os.Getenv("ALLOWED_CLIENT"))
tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(allowed))
```

`source` is passed twice, once as "the cert I present" and once as "the CA I trust". The third argument is the access rule. A client is accepted only if its cert chains to the bundle **and** the URI SAN is exactly `allowed`.

The client mirrors it with `tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(serverID))`. It doesn't check the hostname `service-b`, only the SPIFFE ID in the server's cert.

All of this happens inside the TLS handshake. A rejected caller never sends an HTTP request, and the handler contains no access control. It only reads the caller's ID to say hello:

```go
id, err := spiffetls.PeerIDFromConnectionState(*r.TLS)
```

The three identities:

```
spiffe://example.org/ns/default/sa/service-a    allowed to call service-b
spiffe://example.org/ns/default/sa/service-b    the server
spiffe://example.org/ns/default/sa/service-c    valid identity, not allowed
```

`service-c` is registered on purpose. It has a real SVID signed by the same CA as `service-a`, so when it's rejected, the reason is authorization, not missing or bad credentials.

## Allowed and denied

```
service-b  01:10:30 http: TLS handshake error from 10.244.1.5:54458: unexpected ID "spiffe://example.org/ns/default/sa/service-c"
service-b  01:10:30 request from spiffe://example.org/ns/default/sa/service-a
service-a  01:10:30 200 OK hello spiffe://example.org/ns/default/sa/service-a
service-c  01:10:30 request failed: Get "https://service-b:8443/": remote error: tls: bad certificate
```

- B logs the caller's IP, but the decision doesn't use it. Only the ID in the cert does.
- The real reason (`unexpected ID`) is only in B's log. C just gets `bad certificate`, so a caller can't probe B to learn which IDs are allowed.
- The workloads run without `hostPID` and `hostNetwork`, which the spire-kind-lab workloads had. They still got SVIDs, so the agent does the PID lookup on its own.

Full log in `notes/01-b-allow-a.txt`.

## Breaking it

| #   | What I did                                         | What happened                                                                          |
| --- | -------------------------------------------------- | -------------------------------------------------------------------------------------- |
| 1   | Changed `ALLOWED_CLIENT` to `service-c`             | A rejected, C accepted, within one 5s request cycle. Nothing else changed.             |
| 2   | Changed A's `SERVER_ID` to `service-x`              | A refused the real `service-b`. The rejection moved to the client side.                |
| 3   | Deleted A's entry                                  | A kept working **past its cert's expiry**, until the connection was closed.            |
| 4   | Re-created A's entry                               | A got a new SVID and was accepted again. No restart.                                   |
| 5   | Ran 5 minutes with a new handshake per request     | 62 OK, 0 failed, through about 4 SVID rotations.                                       |

### 1. Swapping the policy

I changed one env var in `service-b.yaml` from `.../sa/service-a` to `.../sa/service-c` and applied it.

```
service-a  01:12:50 200 OK hello spiffe://example.org/ns/default/sa/service-a
service-a  01:12:55 request failed: Get "https://service-b:8443/": remote error: tls: bad certificate

service-c  01:12:50 request failed: Get "https://service-b:8443/": remote error: tls: bad certificate
service-c  01:12:55 200 OK hello spiffe://example.org/ns/default/sa/service-c
```

No IP addresses, firewall rules, client config or certs changed. The rule is written in terms of who the caller is, so it would also survive pods being rescheduled or getting new IPs, which an IP-based rule wouldn't.

The change took effect when Kubernetes replaced the service-b pod. A running server doesn't re-read its environment.

Logs in `notes/02-swap-policy-*.txt`.

### 2. The client distrusting the server

I set `SERVER_ID` in `service-a.yaml` to `spiffe://example.org/ns/default/sa/service-x`. B was the real service-b with a valid cert, and A refused it anyway:

```
service-a  01:14:39 request failed: Get "https://service-b:8443/": unexpected ID "spiffe://example.org/ns/default/sa/service-b"
service-b  01:14:39 http: TLS handshake error from 10.244.1.8:36352: remote error: tls: bad certificate
```

It's experiment 1 in a mirror. The side that rejects logs the real reason, and the other side gets `bad certificate`. Same as the rule in mini-ca-mtls: the side that checks a cert is the side that rejects it.

This is what stops a pod that took over the `service-b` DNS name. Even with a valid SVID from the same trust domain, its ID wouldn't be `service-b`, and A would hang up before sending anything.

Logs in `notes/03-a-distrusts-b-*.txt`.

### 3. Revoking service-a

I deleted A's registration entry while it was calling B. Based on spire-kind-lab, I expected A to keep working until its current SVID expired, at most about two minutes. Instead:

```
service-a  01:18:21 200 OK hello spiffe://example.org/ns/default/sa/service-a
```

That's 24 seconds after A's SVID expired at 01:17:57, and it would have kept going.

The reason is HTTP keep-alive. Go's `http.Client` reused one TLS connection for every request, and certs are only checked during the handshake. A's cert was verified once, when the connection opened, and never looked at again.

I restarted service-b to force a new connection:

```
service-b  01:18:26 http: TLS handshake error from 10.244.1.9:34240: x509svid: could not verify leaf certificate: x509: certificate has expired or is not yet valid: current time 2026-11-06T01:18:26Z is after 2026-11-06T01:17:57Z
service-a  01:18:26 request failed: Get "https://service-b:8443/": remote error: tls: bad certificate
```

A didn't crash. go-spiffe kept the last SVID it had and offered it, and B rejected it as expired. This is a different failure from C's: the cert itself is invalid (authentication), not valid with the wrong name (authorization). The client sees `bad certificate` for both.

So revoking an identity has three layers:

1. Deleting the entry stops new SVIDs within seconds.
2. An SVID already issued stays valid until it expires (TTL).
3. **A connection already open stays usable even after that**, until it closes.

The longest an identity can keep being used after revocation is the TTL plus the lifetime of its connections, not just the TTL.

Logs in `notes/04-revoke-a-*.txt`.

### 4. Restoring it

I re-created the entry with the same ID and selectors:

```
service-a  01:20:01 request failed: Get "https://service-b:8443/": remote error: tls: bad certificate
service-a  01:20:07 200 OK hello spiffe://example.org/ns/default/sa/service-a
```

The agent pushed a new SVID over the Workload API stream, `X509Source` swapped it in, and the next handshake used it. There's no reload code in either service.

Log in `notes/05-restore-a.txt`.

### 5. Rotation with a handshake per request

Because of experiment 3, letting A run for a while didn't prove much about rotation. It was mostly one long connection. I set `DisableKeepAlives: true` on the client's `http.Transport` so that every request does a full mTLS handshake with whatever SVID A has at that moment.

From 01:21:47 to 01:26:53 A made 62 requests, all `200 OK`. With a 2m10s SVID renewed about every minute, that's about four rotations without a single failed handshake.

The one failure in the log is from before the first request succeeded:

```
service-a  01:21:42 request failed: Get "https://service-b:8443/": dial tcp 10.96.152.31:8443: connect: connection refused
```

All three pods restarted together and A tried before B was listening. It's a TCP error and TLS never started. It looks nothing like the identity errors above.

Log in `notes/06-rotation-no-keepalive.txt`.

## Tradeoffs and limits

- **Keep-alive versus revocation.** Long-lived connections are cheaper, but they outlive revocation (experiment 3). Turning keep-alive off fixes that at the cost of a handshake per request. In between, a maximum connection age forces a new handshake every so often.
- **The policy is one ID in an env var.** Fine for a demo, but it doesn't scale. `tlsconfig` also has `AuthorizeOneOf` and `AuthorizeMemberOf` (any ID in a trust domain), and real setups usually put the policy in something like OPA or a service mesh instead of each service's code.
- **Changing the policy restarts the server.** A file or API that the server re-reads would let it change without a restart.
- **Errors are only useful on the rejecting side.** Debugging a `bad certificate` means reading the other service's logs.
- **Everything else is inherited from spire-kind-lab:** `example.org` trust domain, `skip_kubelet_verification`, and Kubernetes RBAC as part of the trust boundary. Anyone who can create a pod in `default` running as `service-a` gets `service-a`'s access.
