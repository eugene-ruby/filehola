# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities privately using GitHub's **Private Vulnerability Reporting** feature.

1. Open the repository's **Security** tab.
2. Navigate to **Advisories**.
3. Select **Report a vulnerability**.
4. Provide a description, reproduction steps, potential impact, and affected versions.

Please **do not disclose security vulnerabilities in public GitHub Issues or Discussions**.

We will review submitted reports and coordinate fixes and disclosure through GitHub Security Advisories.

## General Feedback

For bug reports, feature requests, and general questions, please use [GitHub Issues](../../issues/new).

## Security Considerations

Filehola uses static Bearer Token authentication and does not provide user accounts or role-based access control. Anyone holding the token has full API access. `/health` is public and reports only HTTP process availability. Keep the token out of source control, URLs, shell history, screenshots, and shared logs; rotate it if exposed.

Deploy Filehola behind an appropriate network security boundary. Use TLS at the external boundary whenever the service is accessible over a network. Protect PostgreSQL and S3 credentials and the Bearer token with deployment secrets and restricted access. The Compose credentials are for local development only. Do not expose the optional Swagger UI to untrusted networks.

The MVP limits complete uploads to 50 MiB and uses optimistic revision checks, but it does not provide per-client rate limits, tenant isolation, malware scanning, or a public revision-history API. Review these limitations before using the service with untrusted clients or files. Keep dependency images and Go modules updated through reviewed changes and rerun the integration suite after updates.
