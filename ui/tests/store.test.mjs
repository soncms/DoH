import test from "node:test";
import assert from "node:assert/strict";
import { providerUpstream } from "../src/sondns/api.js";

test("provider mapping keeps the configured SonDNS endpoints", () => {
  assert.equal(providerUpstream("SonCMS").url, "https://dns.soncms.net/dns-query/sondns");
  assert.equal(providerUpstream("NextDNS").url, "https://dns.nextdns.io/2e2e3c");
  assert.equal(providerUpstream("NextDNS").bootstrap.length, 4);
});
