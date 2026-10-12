# Jul.IA — From Vision to a Running Application

Our [brief vision](vision/brief.md) and [longer vision](vision/README.md) explain what we are working toward. This guide explains what you can use now, what you need to bring and how to take the first steps.

This guide reflects our v2.2.0 release. Our [capability status](status.md) and [known limitations](known-limitations.md) provide the current engineering detail.

## When Jul.IA is a useful choice

Choose our gateway when you want to run applications on infrastructure you control and our combination of routing, protection, traffic controls and operations meets your needs.

For example, you might put a dashboard, documentation site and API behind one gateway, give them different routes and access policies, add caching and traffic limits, and inspect their traffic through our Console. The applications can remain in their existing runtimes; our gateway manages requests in front of them.

Our value is in bringing those capabilities together in a standalone product. Where they cover your requirements, you may have fewer components to assemble and maintain. Many individual capabilities are available elsewhere, and another tool may fit your deployment better.

## What you can use today

Our **full release** brings the following capabilities into a portable binary:

| Area | Included capabilities |
| --- | --- |
| Serving and routing | Static websites, reverse proxying, load balancing and health checks. |
| Protection and traffic controls | TLS and mTLS, HTTP Basic, JWT validation, forward auth, rate and request limits, caching, compression and a web application firewall. |
| Protocol gateways | HTTP, native gRPC proxying, supported REST-to-gRPC transcoding, FastCGI/uWSGI and TCP/UDP proxying. |
| Operations | Embedded Console, logs, metrics, tracing, configuration validation and diagnostics; configuration history and rollback in managed deployments. |

We also publish a lean build with fewer bundled capabilities. Choose the full build for the experience described here. Features have different maturity levels, including GA and Beta; consult our status page for the specific capability you intend to rely on.

Installing the binary does not automatically configure authentication, enable every protection or make an application reachable remotely.

## What you need to bring

| Part of your deployment | What you provide |
| --- | --- |
| Application | Website files, or an application already running in its own runtime. We do not install or start its backend. |
| Host | A compatible Windows, Linux or macOS computer or server, with suitable permissions and available ports. A downloaded binary does not require Go or Docker. |
| Connectivity | A path from your users to our gateway: a local network, private network, tunnel or reachable server. |
| Access | Credentials, certificates or an external authentication service where required, plus permissions inside your application. |
| Operation | Configuration, process supervision, updates and backups; writable configuration and state where managed operation requires them. |

For a local preview, you only need the binary, your computer and website files or a running application. A domain, external provider account and remote connectivity service are unnecessary at that stage.

## Start locally

### 1. Download and verify our release

Download the **full archive** for your operating system and architecture from our [releases](https://github.com/victornife/jul/releases), follow the [verification instructions](release.md), and extract it. Open a terminal in the extracted directory.

We publish Windows, Linux and macOS binaries for amd64 and arm64. macOS requires version 13 or later.

### 2. Serve a small website

On Linux or macOS:

```bash
mkdir -p public
printf '<h1>Hello from Jul.IA</h1>\n' > public/index.html
./jul run --serve ./public --listen 127.0.0.1:8080
```

On Windows, in PowerShell:

```powershell
New-Item -ItemType Directory -Force public | Out-Null
Set-Content public/index.html '<h1>Hello from Jul.IA</h1>'
.\jul.exe run --serve .\public --listen 127.0.0.1:8080
```

Open [http://127.0.0.1:8080/](http://127.0.0.1:8080/) in your browser. The explicit loopback address keeps this preview on your computer. Stop it with `Ctrl+C` before running another example on the same port.

### 3. Or put our gateway in front of an application

Start your application first. If it is listening on port 3000 on the same computer, use:

```bash
./jul run --proxy 127.0.0.1:3000 --listen 127.0.0.1:8080
```

On Windows:

```powershell
.\jul.exe run --proxy 127.0.0.1:3000 --listen 127.0.0.1:8080
```

Open the same browser address. Substitute your application's actual address and port if needed.

These commands provide a local preview. They do not create a configuration file, enable the Console or configure restricted remote sharing.

## Configure a deployment and the Console

Our [getting-started guide](getting-started.md) shows how to move to a `server.toml` configuration. Adapt routes, file paths, backend addresses, credentials and certificates to your actual deployment. The bundled sample is a template, not a deployment ready for every host.

On Linux or macOS, validate your configuration and then run it:

```bash
./jul check -config server.toml
./jul serve -config server.toml
```

On Windows, use `.\jul.exe` with the same arguments. Our [deployment guide](deployment.md) covers ongoing operation, including process supervision and filesystem permissions. Validate under the account that will run the service; a successful check does not prove that every backend or external dependency will remain available.

Our full binary includes the Console, but you must enable and protect the admin interface. Keep it on loopback for local administration and configure a strong, unique admin credential. Use private, authenticated access if you need to administer it remotely.

Configuration ownership determines what the Console can do:

| Mode | How configuration changes are made |
| --- | --- |
| `file_owned` — the default | You or your deployment pipeline own the configuration file. The Console can inspect it; configuration mutations are refused. |
| `managed` | Our Console can apply supported changes and use configuration history and rollback. This requires configured admin access, writable configuration and state, and explicit adoption of the initial baseline. |

Use our [configuration reference](configuration.md) and [reload semantics](reload-semantics.md) for supported changes and any restart requirements.

## Choose how people reach your application

| Access arrangement | How it fits with Jul.IA | What remains to configure |
| --- | --- | --- |
| Local or home network | Our gateway listens on a chosen interface and serves files or routes to applications. | Network reachability and any required access policy. Other devices cannot reach the loopback listener used in the preview. |
| Private access with Tailscale | Users reach our gateway through your tailnet, directly or through [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve). | Tailscale installation and network access rules; the appropriate listener or Serve configuration; application permissions. |
| Browser access with Cloudflare Tunnel and Access | [Cloudflare Tunnel](https://developers.cloudflare.com/tunnel/) carries requests to our gateway. [Cloudflare Access](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/) can restrict visitors. | Cloudflare account, hostname, connector and Access policy; origin restrictions that prevent bypass. Tunnel alone does not establish your chosen audience. |
| Direct Internet access | Our gateway receives requests on a reachable server. | Network and firewall routing, a hostname where required, trusted TLS certificates and authentication for restricted services. |
| Public access with Tailscale Funnel | [Funnel](https://tailscale.com/docs/features/tailscale-funnel) can forward public requests to our gateway. | Funnel setup and its protocol and service limits; application or gateway authentication if access must be restricted. Tailnet membership does not restrict public visitors. |

These are composition patterns, rather than a one-click integration we currently provide. Each service retains its own accounts, connectors, identity policies and operating limits. Check that your chosen path carries the protocols you need; an HTTP publishing path does not automatically expose all our protocol capabilities.

## Share from a homelab with appropriate protection

Our gateway can run on homelab hardware. A private network or outbound tunnel can provide reachability without opening inbound router ports for the application. The access policy and origin configuration still matter.

Before sharing:

- Expose only the intended application listener. Keep the admin interface, databases and unrelated services private.
- Choose where authentication is enforced. Configure gateway or application authentication, or an external access service, according to your audience.
- Prevent direct access to backends or alternative paths that bypass that policy. Trust forwarded addresses or identity headers only from the proxy path you have deliberately configured.
- Use appropriate TLS on each connection, maintain the host and applications, and keep recoverable backups of important data and configuration.
- Check that intended users can connect, excluded users cannot, and revoking access has the intended effect.

Gateway controls contribute to a secure deployment. They cannot fix application vulnerabilities or replace permissions inside the application. Our [security guidance](../SECURITY.md) and deployment documentation explain the relevant boundaries.

## How we compare and combine with other tools

| Alternative or companion | When our gateway may add value | When the other tool may be enough |
| --- | --- | --- |
| [Caddy](https://caddyserver.com/docs/quick-starts/reverse-proxy), [NGINX](https://docs.nginx.com/nginx/admin-guide/web-server/reverse-proxy/) or [Traefik](https://doc.traefik.io/traefik/reference/install-configuration/providers/docker/) | You prefer our portable binary and embedded Console for inspecting traffic and managing gateway configuration, and our capabilities cover your needs. | Their capabilities, integrations or established operating model already fit your requirements. These are capable gateways with substantial overlap. |
| Cloudflare Tunnel and Access, or [ngrok](https://ngrok.com/docs/about) | You need application routing, policy or visibility that you operate locally behind the service. | The service and your application already provide the required capabilities. An extra gateway would add little value. |
| Tailscale and Serve | You want additional application gateway controls for services reached privately. | Private connectivity and Serve already cover the use case. |
| [Pangolin](https://docs.pangolin.net/) | You specifically want our standalone gateway and its capabilities. | Integrated connectivity, identity and resource access are your primary need. |
| [Coolify](https://coolify.io/) or another deployment platform | You need our gateway in front of applications the platform runs, with clear ownership of routing and access. | The platform's existing gateway meets your requirements. We do not replace builds, deployments or application lifecycle management. |

Adding Jul.IA should solve a concrete problem. Compare the complete deployment, including maintenance and operating time, when judging cost and complexity.

## What is still missing or outside our current scope

| Area | Our current position | What it means for you |
| --- | --- | --- |
| Integrated sharing | We have not delivered one workflow for publishing, connectivity, invitations, group membership and access changes across providers. | You currently configure and coordinate those services yourself. Group-to-group sharing is our ambition, not an existing federation system. |
| Networking and identity | We do not provide a general VPN, NAT traversal or global relay service, or a complete identity platform. | Use existing connectivity and identity tools where required. |
| Application hosting | We do not provide a managed hosting service or run your application's backend. | Supply its runtime, host and lifecycle management. |
| Coordinated multi-node operation | Our current product is a single-node gateway, without a native fleet control plane or distributed cache and rate-limit state. | Additional infrastructure or another platform is needed when your requirements depend on coordinated state or fleet operation. |

Our release verification provides evidence for published packages and defined deployment journeys. It does not certify every host, network or access arrangement. Our completed [deployment verification journeys](https://github.com/victornife/jul/issues/516) record the checks and their scope.

## Grow, and move on when it serves you

Jul.IA can help you start small, try advanced capabilities and learn what your application needs. Some deployments may remain with our gateway; others may need infrastructure with greater scale, availability or operational support.

Portable configuration and documented interfaces help preserve your choices. They do not make another gateway's configuration interchangeable with ours. A migration may require adapting routes, authentication, certificates, protocol handling and state, then testing the resulting behavior.

Our standalone core is free and open source under AGPL-3.0, without a mandatory Jul-hosted service. Our [open-source commitment](adr/0012-oss-open-core-boundary.md) defines the protected core and room for optional commercial offerings. Hosting, connectivity, external services and operating time remain real costs.

**Start with what you need. Grow as your requirements develop. Keep the freedom to change direction.**

[Read our brief vision](vision/brief.md) · [Read our longer vision](vision/README.md) · [Explore our gateway](https://github.com/victornife/jul)
