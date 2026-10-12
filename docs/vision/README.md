# Jul.IA — Build Anything. Share It on Your Terms.

## Our vision

Make software easier to publish, protect and share, while keeping control over where it runs, how it is shared and who can access it.

## Our mission

Give people and teams the freedom to run and share their own software, with advanced capabilities and control over their infrastructure and dependencies, while reducing unnecessary cost and complexity.

## Why sharing matters more

Useful software often begins with a need close to home: a family dashboard, a tool for a team, a service for a community. AI is making it easier for more people to turn those needs into working applications. Software can become more personal, shaped around the people it serves.

Once an application works, the next challenge is bringing it to those people. They need a way to reach it. You need appropriate access controls and enough visibility to understand what happens when something fails. Depending on the application, that can mean coordinating hosting, networking, certificates, authentication and traffic across several tools. What began as a useful piece of software can become a second infrastructure project.

Good solutions already make simple publishing easy. The difficulty grows when an application needs more capabilities or control, while the people operating it still need a manageable way to keep it running.

That is where **power without unnecessary complexity**, our first principle, matters. A tool used by ten people may hold sensitive information. A community service may need protection against excessive requests. A small application may need caching or configuration changes that can be reversed reliably. Its purpose and the consequences of failure should determine the capabilities available to it. We want to keep those capabilities within reach while making infrastructure, cost and operating effort proportionate to actual needs.

People also need room to choose how their software runs. **Autonomy by design** means supporting the environment that suits them, whether that is a homelab, a private server or cloud infrastructure. It includes using managed services when they help, understanding the dependencies they introduce and retaining control over the parts they operate.

That freedom extends to the audience. **Sharing with chosen groups** means making applications available to the people who need them: family members, collaborators, a community or a team. Our vision for group-to-group sharing extends to communities and teams sharing applications with one another. Private access, selective browser access and public publishing all have a place. We want choosing and managing that audience to become a natural part of sharing software.

## Our gateway is the starting point

Today, we provide a portable, open-source application gateway that brings routing, protection, traffic controls, protocol capabilities and operational visibility together, with an embedded Console. You can use it to serve websites and manage requests to applications you already run, on infrastructure you choose.

Our focus is making that combination easier to operate. It gives people a common place to configure and inspect how traffic reaches their applications. When our capabilities cover their requirements, bringing them together can reduce the separate components they need to assemble and maintain.

The gateway can also fit into connectivity you already use. Tailscale can provide private access to it; Cloudflare Tunnel can provide an external path, with Cloudflare Access where appropriate. We handle configured application traffic, while those services retain their networking and identity responsibilities.

We build within an ecosystem of strong alternatives. Caddy, NGINX and Traefik offer capable gateways. Other options include ngrok for connectivity, Pangolin for networking and identity-based access, and Coolify for deployment and application operation. These tools already solve important parts of the problem, and one of them may meet your needs on its own. Choose Jul.IA when our combination of capabilities, control and operating experience makes your deployment easier to manage.

You still supply the host, application runtime and the connectivity and identity arrangements your deployment requires. Our gateway does not provide a general VPN, a complete identity platform or a managed hosting service. We document [capabilities, maturity and operating limits](../status.md) so you can judge what to rely on.

This is a useful foundation for a broader ambition. We want to make the journey from a running application to an intentionally shared service simpler: establish connectivity, choose an audience, grant or change access, and understand failures. Today, those steps still require coordination across tools. We have not yet brought publishing, invitations, group membership and access management across providers into one integrated experience.

**The gateway is our foundation. Sharing is our destination.**

## A useful middle step, with room to grow

That journey can begin with a few users, an idea to validate or advanced capabilities you want to explore. Jul.IA can provide a useful middle step before a larger platform is justified, and we want to support growth while our capabilities fit.

Some applications may stay with us indefinitely. Others will need infrastructure with greater scale, availability or operational support, along with its associated costs and responsibilities. Moving on is a good outcome when it serves the application and its users. Some applications will need that infrastructure from the outset.

Our commitment to portable configuration and documented interfaces supports that freedom. Migration can still require adaptation and testing; we aim to make the differences understandable and avoid artificial barriers. Helping people grow includes preserving their freedom to move on.

## Our commitment

Our standalone core is free, open-source software under AGPL-3.0, without a mandatory Jul-hosted control plane. Our [open-source commitment](../adr/0012-oss-open-core-boundary.md) protects its functionality, security fixes and configuration portability. Optional commercial or hosted services may add value around that core.

Running software still takes hardware, connectivity and operating time. External services may bring additional costs, and a managed service can sometimes reduce the total work and expense. We want people to understand those trade-offs and choose what serves them.

The same honesty applies to protection. Gateway controls contribute to a secure deployment; they cannot make a vulnerable application safe. Application permissions, backups and host maintenance remain essential. Security, performance and simplicity are engineering commitments we must demonstrate, with evidence and clear operating boundaries.

Our ambition is for more useful software to reach the people who need it, with the capabilities its purpose requires and fewer barriers between building it and sharing it.

**Build it. Run it where you choose. Share it on your terms.**

## Ready for the details?

Our [practical guide](../practical-guide.md) explains what you can use today, what you need to bring, how we fit alongside existing tools and how to start.
