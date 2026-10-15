# Admin OIDC contract and integration fixtures

Public discovery JSON was retrieved on 2026-10-15 from the source URL recorded
in each wrapper. Metadata contains no user, tenant, token or secret and is used
only as dated protocol-contract data. Ordinary tests read committed fixtures
without calling public providers. A passing fixture test is not a provider-account
login result. Google is a positive discovery shape; Meta's current metadata is a
negative Code-flow contract despite its separately documented testing flow.

The pinned Keycloak 26.5.7 realm uses synthetic test-only users, credentials,
identifiers and groups. It is imported only into the disposable loopback test
container by `scripts/admin-oidc-keycloak.sh`; never import it into a real realm.
The script records the resolved image digest. Do not commit actual provider
credentials or private claims here. See [the operator guide](../../docs/admin-oidc.md)
for the compatibility matrix and exact integration boundaries.
