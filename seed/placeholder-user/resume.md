# Robin Ayers

Backend engineer, seven years. Invented for voice-guide's development and tests; any likeness to a real person is accidental.

## Harbourline Logistics, senior backend engineer (2023 to now)
- Led the rebuild of shipment tracking: moved status updates from nightly batch files to an event stream, so customers saw a parcel's state within a minute instead of the next morning.
- Cut the tracking API's 99th-percentile latency from 900 ms to 120 ms by replacing per-request joins with a read model kept up to date from the stream.
- Ran the on-call rotation for six engineers; wrote the runbooks that halved the time to resolve the most common pages.

## Tidewater Books, backend engineer (2020 to 2023)
- Built the order service for a bookshop chain's online store, from the first line to 40,000 orders a day.
- Made checkout safe to retry: every order carries a client-chosen key, so a customer who pressed "buy" twice was charged once.
- Moved the catalogue search from the main database to a search index, which ended the evening slowdowns during sales.

## Ferrow Analytics, junior developer (2018 to 2020)
- Wrote the importers that loaded clients' spreadsheets into the reporting database, with validation that told each client which rows were wrong and why.

## Skills
Go, PostgreSQL, Kafka, Kubernetes, Python. Comfortable designing APIs and data models; still learning large-scale frontend work.
