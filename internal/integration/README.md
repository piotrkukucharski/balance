# Financial Integrations (`internal/integration`)

## Overview
Intermediate blueprint package for financial integrations, abstracting statement CSV ingestion, Open Banking PSD2 feeds via GoCardless Bank Account Data API, and deterministic offline mock banking providers.

## Subdirectories & Boundaries
- [CSV Importer](./csv/README.md): Bank statement CSV parser and importer with automatic encoding and delimiter sniffing.
- [GoCardless Open Banking](./gocardless/README.md): GoCardless Bank Account Data API v2 client for PSD2 open banking feeds.
- [Mock Bank Adapter](./mock/README.md): Deterministic mock banking adapter for offline development and testing.
