# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: **Security > Report a vulnerability** on this
repository. Do not open a public issue. The maintainer must enable private vulnerability
reporting in the repository settings for this to work.

## Supported versions

Only `main` is supported; fixes land there.

## Deployment note

The backend mounts the host Docker socket, which is host-root equivalent (see the README).
