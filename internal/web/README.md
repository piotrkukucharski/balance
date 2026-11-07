# Web Presentation Subsystem (`internal/web`)

## Overview
Presentation layer orchestrating HTTP routing, controllers, embedded HTML views, and static assets for the Balance web application.

## Subdirectories & Boundaries
- [HTTP Handlers](./handlers/README.md): HTTP web controllers, session/bearer authentication middlewares, CSRF validators, and server-side rendered endpoints.
- [HTML Templates & Assets Engine](./templates/README.md): HTML template registry and static asset distribution engine using embedded files, SHA-256 content hashing, and localized formatters.
