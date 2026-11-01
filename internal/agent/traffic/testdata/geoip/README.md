# GeoIP test databases

`GeoIP2-City-Test.mmdb` (18 KB) and `GeoIP2-Country-Test.mmdb` (17 KB) are
unmodified copies of the official test data of
[maxmind/MaxMind-DB](https://github.com/maxmind/MaxMind-DB/tree/main/test-data)
(`test-data/`). They hold a few dozen networks with made-up and example
records, for example `81.2.69.142` → London, GB and `216.160.83.56` → Milton, US.

Licence: the MaxMind-DB repository is "free software, licensed under the
Apache License, Version 2.0 or the MIT License, at your option"
(`LICENSE-APACHE`, `LICENSE-MIT` in that repository). Copyright © 2013–2026
MaxMind, Inc.

They are used only by tests in `internal/agent/traffic`. No production GeoIP
database is ever stored in this repository: the agent downloads DB-IP Lite or
MaxMind GeoLite2 into `<state-dir>/geoip/` at runtime.
